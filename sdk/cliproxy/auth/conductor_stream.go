package auth

import (
	"context"
	"net/http"
	"strings"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func discardStreamChunks(ch <-chan cliproxyexecutor.StreamChunk) {
	if ch == nil {
		return
	}
	go func() {
		for range ch {
		}
	}()
}

type streamBootstrapError struct {
	cause   error
	headers http.Header
}

func cloneHTTPHeader(headers http.Header) http.Header {
	if headers == nil {
		return nil
	}
	return headers.Clone()
}

func newStreamBootstrapError(err error, headers http.Header) error {
	if err == nil {
		return nil
	}
	return &streamBootstrapError{
		cause:   err,
		headers: cloneHTTPHeader(headers),
	}
}

func (e *streamBootstrapError) Error() string {
	if e == nil || e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

func (e *streamBootstrapError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *streamBootstrapError) Headers() http.Header {
	if e == nil {
		return nil
	}
	return cloneHTTPHeader(e.headers)
}

func streamErrorResult(headers http.Header, err error) *cliproxyexecutor.StreamResult {
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Err: err}
	close(ch)
	return &cliproxyexecutor.StreamResult{
		Headers: cloneHTTPHeader(headers),
		Chunks:  ch,
	}
}

func validateStreamResult(result *cliproxyexecutor.StreamResult, err error) (*cliproxyexecutor.StreamResult, error) {
	if err != nil {
		return result, err
	}
	if result == nil || result.Chunks == nil {
		return result, &Error{Code: "empty_stream", Message: "upstream stream has no source", Retryable: true}
	}
	return result, nil
}

func readStreamBootstrap(ctx context.Context, ch <-chan cliproxyexecutor.StreamChunk) ([]cliproxyexecutor.StreamChunk, bool, error) {
	if ch == nil {
		return nil, true, nil
	}
	buffered := make([]cliproxyexecutor.StreamChunk, 0, 1)
	for {
		var (
			chunk cliproxyexecutor.StreamChunk
			ok    bool
		)
		if ctx != nil {
			select {
			case <-ctx.Done():
				return nil, false, ctx.Err()
			case chunk, ok = <-ch:
			}
		} else {
			chunk, ok = <-ch
		}
		if !ok {
			return buffered, true, nil
		}
		if chunk.Err != nil {
			return nil, false, chunk.Err
		}
		buffered = append(buffered, chunk)
		if len(chunk.Payload) > 0 {
			return buffered, false, nil
		}
	}
}

func (m *Manager) wrapStreamResult(ctx context.Context, auth *Auth, provider, resultModel string, headers http.Header, buffered []cliproxyexecutor.StreamChunk, remaining <-chan cliproxyexecutor.StreamChunk, aliasResult OAuthModelAliasResult, ephemeralResult bool, opts cliproxyexecutor.Options, streamStart time.Time, firstEvent time.Duration, adaptiveLease *adaptiveAuthLease) *cliproxyexecutor.StreamResult {
	out := make(chan cliproxyexecutor.StreamChunk)
	if streamStart.IsZero() {
		streamStart = time.Now()
	}
	go func() {
		defer func() {
			if adaptiveLease != nil {
				adaptiveLease.release(firstEvent, time.Since(streamStart), true)
			}
			close(out)
		}()
		var failed bool
		forward := true
		var rewriter *StreamRewriter
		if aliasResult.ForceMapping && strings.TrimSpace(aliasResult.OriginalAlias) != "" {
			rewriter = NewStreamRewriter(StreamRewriteOptions{RewriteModel: aliasResult.OriginalAlias})
		}
		emit := func(chunk cliproxyexecutor.StreamChunk) bool {
			if chunk.Err != nil && !failed {
				failed = true
				if adaptiveLease != nil {
					adaptiveLease.release(firstEvent, time.Since(streamStart), false)
					adaptiveLease = nil
				}
				entry := logEntryWithRequestID(ctx)
				warnLogUpstreamFailure(ctx, entry, provider, resultModel, auth, time.Since(streamStart), chunk.Err)
				rerr := resultErrorFromError(chunk.Err)
				action, okAction := matchRequestScopedErrorAction(auth, chunk.Err, m.runtimeConfigSnapshot())
				result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: false, Error: rerr, Options: opts}
				applyRequestScopedActionToResult(action, okAction, &result)
				m.recordExecutionResult(ctx, result, auth, ephemeralResult)
			}
			if !forward {
				return false
			}
			if chunk.Err != nil {
				if ctx == nil {
					out <- chunk
					return true
				}
				select {
				case <-ctx.Done():
					forward = false
					return false
				case out <- chunk:
					return true
				}
			}
			if len(chunk.Payload) == 0 {
				return true
			}
			payload := rewriteForceMappedStreamChunk(rewriter, chunk.Payload)
			if len(payload) == 0 {
				return true
			}
			chunk.Payload = payload
			if ctx == nil {
				out <- chunk
				return true
			}
			select {
			case <-ctx.Done():
				forward = false
				return false
			case out <- chunk:
				return true
			}
		}
		for _, chunk := range buffered {
			if ok := emit(chunk); !ok {
				discardStreamChunks(remaining)
				return
			}
		}
		for chunk := range remaining {
			if ok := emit(chunk); !ok {
				discardStreamChunks(remaining)
				return
			}
		}
		if tail := finishForceMappedStreamChunks(rewriter); len(tail) > 0 {
			tailChunk := cliproxyexecutor.StreamChunk{Payload: tail}
			if !emit(tailChunk) {
				return
			}
		}
		if !failed && (ephemeralResult || claudeOAuthRequestCancellation(ctx, auth, nil) == nil) {
			m.recordExecutionResult(ctx, Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: true, Options: opts}, auth, ephemeralResult)
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: headers, Chunks: out}
}

func (m *Manager) replaceHomeExecutionLifecycleAuth(lifecycle cliproxyexecutor.ExecutionLifecycle, auth *Auth) {
	selection, ok := lifecycle.(*HomeDispatchSelection)
	if !ok || selection == nil {
		return
	}
	m.replaceHomeSelectionAuth(selection, auth)
}

func (m *Manager) executeStreamWithModelPool(ctx context.Context, executor ProviderExecutor, auth *Auth, provider string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, routeModel, executionModel string, execModels []string, pooled bool, aliasResult OAuthModelAliasResult, routing *apiKeyModelRoutingSnapshot, allowRetry bool, ephemeralResult bool, unauthorizedRefreshTried map[string]struct{}) (*cliproxyexecutor.StreamResult, error) {
	if executor == nil {
		return nil, &Error{Code: "executor_not_found", Message: "executor not registered"}
	}
	ctx = contextWithRequestedModelAlias(ctx, opts, routeModel)
	var lastErr error
	didRefreshOnUnauthorized := false
	if auth != nil && unauthorizedRefreshTried != nil {
		_, didRefreshOnUnauthorized = unauthorizedRefreshTried[auth.ID]
	}
	for idx, execModel := range execModels {
		ctx = newUpstreamAttemptContext(ctx)
		resultModel := m.stateModelForExecution(auth, routeModel, execModel, pooled)
		execReq := req
		execReq.Model = execModel
		if executionModel != "" {
			execReq.Model = executionModel
		}
		execOpts := opts
		var errIntercept error
		execReq, execOpts, errIntercept = applyRequestAfterAuthInterceptor(ctx, executor, provider, execReq, execOpts, requestedModelAliasFromOptions(execOpts, routeModel))
		if errIntercept != nil {
			return nil, errIntercept
		}
		if executionModel == "" {
			execReq = attachResolvedAPIKeyModelInfo(routing, execReq, auth, routeModel, execModel)
		}
		if errCtx := ctx.Err(); errCtx != nil {
			return nil, errCtx
		}
		entry := logEntryWithRequestID(ctx)
		startStream := time.Now()
		adaptiveLease := m.scheduler.acquireAdaptive(provider, resultModel, auth.ID)
		streamResult, errStream := executor.ExecuteStream(ctx, auth, execReq, execOpts)
		durationStream := time.Since(startStream)
		if errStream != nil {
			if adaptiveLease != nil {
				adaptiveLease.release(0, durationStream, false)
				adaptiveLease = nil
			}
			if IsCodexReplicaConcurrencyError(errStream) {
				return nil, errStream
			}
			if errCtx := ctx.Err(); errCtx != nil {
				return nil, errCtx
			}
			if allowRetry {
				alreadyTried := didRefreshOnUnauthorized
				willAttemptHomeRefresh := ephemeralResult && !alreadyTried && auth != nil && auth.AuthKind() == AuthKindOAuth && isUnauthorizedError(errStream)
				refreshCtx := newUpstreamAttemptContext(ctx)
				refreshed, okRefresh, errRefresh := m.tryRefreshExecutionAuthAfterUnauthorized(refreshCtx, executor, auth, errStream, alreadyTried, ephemeralResult)
				if willAttemptHomeRefresh {
					didRefreshOnUnauthorized = true
					if unauthorizedRefreshTried != nil {
						unauthorizedRefreshTried[auth.ID] = struct{}{}
					}
				}
				if errRefresh != nil {
					errStream = errRefresh
					warnLogUpstreamFailure(ctx, entry, provider, execModel, auth, durationStream, errStream)
				} else if okRefresh {
					auth = refreshed
					adaptiveLease = m.scheduler.acquireAdaptive(provider, resultModel, auth.ID)
					m.replaceHomeExecutionLifecycleAuth(execOpts.ExecutionLifecycle, auth)
					publishSelectedAuthMetadata(execOpts.Metadata, auth)
					didRefreshOnUnauthorized = true
					ctx = newUpstreamAttemptContext(ctx)
					startRetry := time.Now()
					startStream = startRetry
					streamResult, errStream = executor.ExecuteStream(ctx, auth, execReq, execOpts)
					durationRetry := time.Since(startRetry)
					if errStream != nil {
						if adaptiveLease != nil {
							adaptiveLease.release(0, durationRetry, false)
							adaptiveLease = nil
						}
						warnLogUpstreamFailure(ctx, entry, provider, execModel, auth, durationRetry, errStream)
						if errCtx := ctx.Err(); errCtx != nil {
							return nil, errCtx
						}
					}
				} else {
					warnLogUpstreamFailure(ctx, entry, provider, execModel, auth, durationStream, errStream)
				}
			} else {
				warnLogUpstreamFailure(ctx, entry, provider, execModel, auth, durationStream, errStream)
			}
		}
		if !ephemeralResult {
			if errCancel := claudeOAuthRequestCancellation(ctx, auth, errStream); errCancel != nil {
				if adaptiveLease != nil {
					adaptiveLease.release(0, time.Since(startStream), false)
					adaptiveLease = nil
				}
				return nil, errCancel
			}
		}
		streamResult, errStream = validateStreamResult(streamResult, errStream)
		if errStream != nil {
			if adaptiveLease != nil {
				adaptiveLease.release(0, time.Since(startStream), false)
				adaptiveLease = nil
			}
			rerr := resultErrorFromError(errStream)
			action, okAction := matchRequestScopedErrorAction(auth, errStream, m.runtimeConfigSnapshot())
			result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: false, Error: rerr, Options: execOpts}
			result.RetryAfter = retryAfterFromError(errStream)
			if isCredentialScopedError(errStream) {
				result.CredentialScope = true
			}
			applyRequestScopedActionToResult(action, okAction, &result)
			m.recordExecutionResult(ctx, result, auth, ephemeralResult)
			if okAction {
				if isRequestScopedStop(action, okAction) {
					return nil, wrapRequestStopError(errStream)
				}
				lastErr = errStream
				if result.CredentialScope {
					return nil, errStream
				}
				continue
			}
			if isRequestInvalidError(errStream) {
				return nil, errStream
			}
			lastErr = errStream
			if result.CredentialScope {
				return nil, errStream
			}
			continue
		}

		buffered, closed, bootstrapErr := readStreamBootstrap(ctx, streamResult.Chunks)
		if bootstrapErr != nil {
			if adaptiveLease != nil {
				adaptiveLease.release(0, time.Since(startStream), false)
				adaptiveLease = nil
			}
			if errCtx := ctx.Err(); errCtx != nil {
				discardStreamChunks(streamResult.Chunks)
				return nil, errCtx
			}
			if allowRetry {
				alreadyTried := didRefreshOnUnauthorized
				willAttemptHomeRefresh := ephemeralResult && !alreadyTried && auth != nil && auth.AuthKind() == AuthKindOAuth && isUnauthorizedError(bootstrapErr)
				refreshCtx := newUpstreamAttemptContext(ctx)
				refreshed, okRefresh, errRefresh := m.tryRefreshExecutionAuthAfterUnauthorized(refreshCtx, executor, auth, bootstrapErr, alreadyTried, ephemeralResult)
				if willAttemptHomeRefresh {
					didRefreshOnUnauthorized = true
					if unauthorizedRefreshTried != nil {
						unauthorizedRefreshTried[auth.ID] = struct{}{}
					}
				}
				if errRefresh != nil {
					discardStreamChunks(streamResult.Chunks)
					bootstrapErr = errRefresh
					warnLogUpstreamFailure(ctx, entry, provider, execModel, auth, time.Since(startStream), bootstrapErr)
					streamResult = &cliproxyexecutor.StreamResult{}
				} else if okRefresh {
					discardStreamChunks(streamResult.Chunks)
					auth = refreshed
					adaptiveLease = m.scheduler.acquireAdaptive(provider, resultModel, auth.ID)
					m.replaceHomeExecutionLifecycleAuth(execOpts.ExecutionLifecycle, auth)
					publishSelectedAuthMetadata(execOpts.Metadata, auth)
					didRefreshOnUnauthorized = true
					ctx = newUpstreamAttemptContext(ctx)
					startRetry := time.Now()
					startStream = startRetry
					retryStream, retryErr := executor.ExecuteStream(ctx, auth, execReq, execOpts)
					retryStream, retryErr = validateStreamResult(retryStream, retryErr)
					if retryErr != nil {
						if adaptiveLease != nil {
							adaptiveLease.release(0, time.Since(startRetry), false)
							adaptiveLease = nil
						}
						if errCtx := ctx.Err(); errCtx != nil {
							return nil, errCtx
						}
						bootstrapErr = retryErr
						warnLogUpstreamFailure(ctx, entry, provider, execModel, auth, time.Since(startRetry), bootstrapErr)
						streamResult = &cliproxyexecutor.StreamResult{}
					} else {
						streamResult = retryStream
						startStream = startRetry
						buffered, closed, bootstrapErr = readStreamBootstrap(ctx, streamResult.Chunks)
						if bootstrapErr != nil {
							if adaptiveLease != nil {
								adaptiveLease.release(0, time.Since(startRetry), false)
								adaptiveLease = nil
							}
							warnLogUpstreamFailure(ctx, entry, provider, execModel, auth, time.Since(startRetry), bootstrapErr)
						}
					}
				} else {
					warnLogUpstreamFailure(ctx, entry, provider, execModel, auth, time.Since(startStream), bootstrapErr)
				}
			} else {
				warnLogUpstreamFailure(ctx, entry, provider, execModel, auth, time.Since(startStream), bootstrapErr)
			}
		}
		if !ephemeralResult {
			if errCancel := claudeOAuthRequestCancellation(ctx, auth, bootstrapErr); errCancel != nil {
				if adaptiveLease != nil {
					adaptiveLease.release(0, time.Since(startStream), false)
					adaptiveLease = nil
				}
				discardStreamChunks(streamResult.Chunks)
				return nil, errCancel
			}
		}
		if bootstrapErr != nil {
			if adaptiveLease != nil {
				adaptiveLease.release(0, time.Since(startStream), false)
				adaptiveLease = nil
			}
			action, okAction := matchRequestScopedErrorAction(auth, bootstrapErr, m.runtimeConfigSnapshot())
			if okAction {
				rerr := resultErrorFromError(bootstrapErr)
				result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: false, Error: rerr, Options: execOpts}
				result.RetryAfter = retryAfterFromError(bootstrapErr)
				if isCredentialScopedError(bootstrapErr) {
					result.CredentialScope = true
				}
				applyRequestScopedActionToResult(action, okAction, &result)
				m.recordExecutionResult(ctx, result, auth, ephemeralResult)
				discardStreamChunks(streamResult.Chunks)
				if isRequestScopedStop(action, okAction) {
					return nil, wrapRequestStopError(bootstrapErr)
				}
				lastErr = bootstrapErr
				if result.CredentialScope {
					return nil, newStreamBootstrapError(bootstrapErr, streamResult.Headers)
				}
				continue
			}
			if isRequestInvalidError(bootstrapErr) {
				rerr := resultErrorFromError(bootstrapErr)
				result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: false, Error: rerr, Options: execOpts}
				result.RetryAfter = retryAfterFromError(bootstrapErr)
				if isCredentialScopedError(bootstrapErr) {
					result.CredentialScope = true
				}
				m.recordExecutionResult(ctx, result, auth, ephemeralResult)
				discardStreamChunks(streamResult.Chunks)
				return nil, bootstrapErr
			}
			if idx < len(execModels)-1 {
				rerr := resultErrorFromError(bootstrapErr)
				result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: false, Error: rerr, Options: execOpts}
				result.RetryAfter = retryAfterFromError(bootstrapErr)
				if isCredentialScopedError(bootstrapErr) {
					result.CredentialScope = true
				}
				m.recordExecutionResult(ctx, result, auth, ephemeralResult)
				discardStreamChunks(streamResult.Chunks)
				lastErr = bootstrapErr
				if result.CredentialScope {
					return nil, newStreamBootstrapError(bootstrapErr, streamResult.Headers)
				}
				continue
			}
			rerr := resultErrorFromError(bootstrapErr)
			result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: false, Error: rerr, Options: execOpts}
			result.RetryAfter = retryAfterFromError(bootstrapErr)
			if isCredentialScopedError(bootstrapErr) {
				result.CredentialScope = true
			}
			m.recordExecutionResult(ctx, result, auth, ephemeralResult)
			discardStreamChunks(streamResult.Chunks)
			return nil, newStreamBootstrapError(bootstrapErr, streamResult.Headers)
		}

		if closed && len(buffered) == 0 {
			if adaptiveLease != nil {
				adaptiveLease.release(0, time.Since(startStream), false)
				adaptiveLease = nil
			}
			emptyErr := &Error{Code: "empty_stream", Message: "upstream stream closed before first payload", Retryable: true}
			warnLogUpstreamFailure(ctx, entry, provider, execModel, auth, time.Since(startStream), emptyErr)
			result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: false, Error: emptyErr, Options: execOpts}
			m.recordExecutionResult(ctx, result, auth, ephemeralResult)
			if idx < len(execModels)-1 {
				lastErr = emptyErr
				continue
			}
			return nil, newStreamBootstrapError(emptyErr, streamResult.Headers)
		}

		remaining := streamResult.Chunks
		if closed {
			closedCh := make(chan cliproxyexecutor.StreamChunk)
			close(closedCh)
			remaining = closedCh
		}
		attemptAliasResult := resolveAttemptAliasResult(routing, auth, routeModel, execModel, aliasResult)
		firstEvent := time.Since(startStream)
		return m.wrapStreamResult(ctx, auth.Clone(), provider, resultModel, streamResult.Headers, buffered, remaining, attemptAliasResult, ephemeralResult, execOpts, startStream, firstEvent, adaptiveLease), nil
	}
	if lastErr == nil {
		lastErr = &Error{Code: "auth_not_found", Message: "no upstream model available"}
	}
	return nil, lastErr
}
