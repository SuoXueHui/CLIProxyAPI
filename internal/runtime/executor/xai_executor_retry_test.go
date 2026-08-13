package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

const xaiRetryCompletedEvent = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_retry\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"model\":\"grok-4.5\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"

func executeXAINonStreamRetryTest(t *testing.T, serverURL string) (cliproxyexecutor.Response, error) {
	t.Helper()
	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider:   "xai",
		Attributes: map[string]string{"base_url": serverURL},
		Metadata:   map[string]any{"access_token": "xai-token"},
	}
	return exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-4.5",
		Payload: []byte(`{"model":"grok-4.5","input":"hello"}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
}

func TestXAIExecutorExecuteRetriesIncompleteNonStreamResponseOnce(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "text/event-stream")
		if attempts == 1 {
			_, _ = w.Write([]byte("data: {\"type\":\"response.created\"}\n\n"))
			return
		}
		_, _ = w.Write([]byte(xaiRetryCompletedEvent))
	}))
	defer server.Close()

	resp, err := executeXAINonStreamRetryTest(t, server.URL)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("upstream attempts = %d, want 2", attempts)
	}
	if !gjson.ValidBytes(resp.Payload) {
		t.Fatalf("response payload is not valid JSON: %s", string(resp.Payload))
	}
}

func TestXAIExecutorExecuteStopsAfterOneIncompleteNonStreamRetry(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\"}\n\n"))
	}))
	defer server.Close()

	_, err := executeXAINonStreamRetryTest(t, server.URL)
	if err == nil {
		t.Fatal("Execute() error = nil, want incomplete stream error")
	}
	status, ok := err.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != http.StatusRequestTimeout {
		t.Fatalf("Execute() error = %#v, want status 408", err)
	}
	var lifecycle interface{ IsConnectionLifecycle() bool }
	if !errors.As(err, &lifecycle) || lifecycle == nil || !lifecycle.IsConnectionLifecycle() {
		t.Fatalf("Execute() error = %#v, want connection lifecycle marker", err)
	}
	if attempts != 2 {
		t.Fatalf("upstream attempts = %d, want 2", attempts)
	}
}

func TestXAIExecutorExecuteRetriesNonStreamBodyReadFailureOnce(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts == 1 {
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Error("response writer does not support hijacking")
				return
			}
			conn, _, errHijack := hijacker.Hijack()
			if errHijack != nil {
				t.Errorf("hijack connection: %v", errHijack)
				return
			}
			_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: 1024\r\nConnection: close\r\n\r\ndata: {\"type\":\"response.created\"}\n\n"))
			_ = conn.Close()
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(xaiRetryCompletedEvent))
	}))
	defer server.Close()

	_, err := executeXAINonStreamRetryTest(t, server.URL)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("upstream attempts = %d, want 2", attempts)
	}
}

func TestXAIExecutorExecuteFinalNonStreamBodyReadFailureIsConnectionLifecycle(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("response writer does not support hijacking")
			return
		}
		conn, _, errHijack := hijacker.Hijack()
		if errHijack != nil {
			t.Errorf("hijack connection: %v", errHijack)
			return
		}
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: 1024\r\nConnection: close\r\n\r\ndata: {\"type\":\"response.created\"}\n\n"))
		_ = conn.Close()
	}))
	defer server.Close()

	_, err := executeXAINonStreamRetryTest(t, server.URL)
	if err == nil {
		t.Fatal("Execute() error = nil, want body disconnect error")
	}
	status, ok := err.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != http.StatusRequestTimeout {
		t.Fatalf("Execute() error = %#v, want status 408", err)
	}
	if err.Error() != xaiIncompleteStreamMessage {
		t.Fatalf("Execute() error = %q, want %q", err.Error(), xaiIncompleteStreamMessage)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("Execute() error = %#v, want wrapped unexpected EOF", err)
	}
	var lifecycle interface{ IsConnectionLifecycle() bool }
	if !errors.As(err, &lifecycle) || lifecycle == nil || !lifecycle.IsConnectionLifecycle() {
		t.Fatalf("Execute() error = %#v, want connection lifecycle marker", err)
	}
	if attempts != 2 {
		t.Fatalf("upstream attempts = %d, want 2", attempts)
	}
}

func TestXAIExecutorExecuteDoesNotRetryHTTPStatusError(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		http.Error(w, `{"error":{"message":"unavailable"}}`, http.StatusServiceUnavailable)
	}))
	defer server.Close()

	_, err := executeXAINonStreamRetryTest(t, server.URL)
	if err == nil {
		t.Fatal("Execute() error = nil, want upstream status error")
	}
	if attempts != 1 {
		t.Fatalf("upstream attempts = %d, want 1", attempts)
	}
}
