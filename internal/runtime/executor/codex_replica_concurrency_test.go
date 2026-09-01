package executor

import (
	"context"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestCodexAutoExecutorRejectsSaturatedReplicaBeforeExecution(t *testing.T) {
	auth := &cliproxyauth.Auth{
		ID:       "codex.json::replica:1",
		Provider: "codex",
		Attributes: map[string]string{
			cliproxyauth.AttributeCodexReplicaGroup:       "codex.json",
			cliproxyauth.AttributeCodexReplicaIndex:       "1",
			cliproxyauth.AttributeCodexReplicaCount:       "6",
			cliproxyauth.AttributeCodexReplicaConcurrency: "1",
		},
	}
	occupied, errAcquire := cliproxyauth.AcquireCodexReplicaConcurrency(auth)
	if errAcquire != nil || occupied == nil {
		t.Fatalf("occupy replica = (%v, %v)", occupied, errAcquire)
	}
	defer occupied.Release()

	executor := &CodexAutoExecutor{
		httpExec: &CodexExecutor{},
		wsExec:   &CodexWebsocketsExecutor{},
	}
	request := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":"test"}`)}
	if _, errExecute := executor.Execute(context.Background(), auth, request, cliproxyexecutor.Options{}); !cliproxyauth.IsCodexReplicaConcurrencyError(errExecute) {
		t.Fatalf("Execute() error = %v, want replica concurrency error", errExecute)
	}
	if _, errStream := executor.ExecuteStream(context.Background(), auth, request, cliproxyexecutor.Options{}); !cliproxyauth.IsCodexReplicaConcurrencyError(errStream) {
		t.Fatalf("ExecuteStream() error = %v, want replica concurrency error", errStream)
	}
}

func TestCodexReplicaStreamHoldsSlotUntilTerminalChunk(t *testing.T) {
	auth := &cliproxyauth.Auth{
		ID:       "codex-stream.json::replica:1",
		Provider: "codex",
		Attributes: map[string]string{
			cliproxyauth.AttributeCodexReplicaGroup:       "codex-stream.json",
			cliproxyauth.AttributeCodexReplicaIndex:       "1",
			cliproxyauth.AttributeCodexReplicaCount:       "1",
			cliproxyauth.AttributeCodexReplicaConcurrency: "1",
		},
	}
	lease, errAcquire := cliproxyauth.AcquireCodexReplicaConcurrency(auth)
	if errAcquire != nil || lease == nil {
		t.Fatalf("AcquireCodexReplicaConcurrency() = (%v, %v)", lease, errAcquire)
	}
	upstream := make(chan cliproxyexecutor.StreamChunk, 1)
	wrapped := wrapCodexReplicaStream(context.Background(), &cliproxyexecutor.StreamResult{Chunks: upstream}, lease)
	if second, errSecond := cliproxyauth.AcquireCodexReplicaConcurrency(auth); second != nil || !cliproxyauth.IsCodexReplicaConcurrencyError(errSecond) {
		t.Fatalf("slot while stream open = (%v, %v), want concurrency error", second, errSecond)
	}
	upstream <- cliproxyexecutor.StreamChunk{Payload: []byte("done")}
	close(upstream)
	for range wrapped.Chunks {
	}
	replacement, errReplacement := cliproxyauth.AcquireCodexReplicaConcurrency(auth)
	if errReplacement != nil || replacement == nil {
		t.Fatalf("slot after terminal chunk = (%v, %v), want lease", replacement, errReplacement)
	}
	replacement.Release()
}
