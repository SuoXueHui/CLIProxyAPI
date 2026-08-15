package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorWeeklyOverdraftObserveAndInject(t *testing.T) {
	tests := []struct {
		name      string
		mode      string
		wantItems int
	}{
		{name: "observe", mode: config.CodexWeeklyOverdraftModeObserve, wantItems: 1},
		{name: "inject", mode: config.CodexWeeklyOverdraftModeInject, wantItems: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			beforeSuccess := helps.CodexWeeklyOverdraftStatusSnapshot().Outcomes.Success
			captured := make(chan []byte, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Errorf("read request: %v", errRead)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				captured <- bytes.Clone(body)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
			}))
			defer server.Close()

			cfg := weeklyOverdraftIntegrationConfig(test.mode)
			executor := NewCodexExecutor(&cfg)
			result, errExecute := executor.ExecuteStream(context.Background(), weeklyOverdraftIntegrationAuth(server.URL), weeklyOverdraftIntegrationRequest(), weeklyOverdraftIntegrationOptions())
			if errExecute != nil {
				t.Fatalf("ExecuteStream() error = %v", errExecute)
			}
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatalf("stream error = %v", chunk.Err)
				}
			}
			if got := helps.CodexWeeklyOverdraftStatusSnapshot().Outcomes.Success; got != beforeSuccess+1 {
				t.Fatalf("success outcomes = %d, want %d", got, beforeSuccess+1)
			}

			select {
			case body := <-captured:
				if got := len(gjson.GetBytes(body, "input").Array()); got != test.wantItems {
					t.Fatalf("upstream input items = %d, want %d; body=%s", got, test.wantItems, body)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for HTTP upstream payload")
			}
		})
	}
}

func TestCodexExecutorWeeklyOverdraftRecordsUsageLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"type":"usage_limit_reached","message":"quota reached"}}`))
	}))
	defer server.Close()

	before := helps.CodexWeeklyOverdraftStatusSnapshot().Outcomes.UsageLimit
	cfg := weeklyOverdraftIntegrationConfig(config.CodexWeeklyOverdraftModeInject)
	executor := NewCodexExecutor(&cfg)
	_, errExecute := executor.ExecuteStream(context.Background(), weeklyOverdraftIntegrationAuth(server.URL), weeklyOverdraftIntegrationRequest(), weeklyOverdraftIntegrationOptions())
	if errExecute == nil {
		t.Fatal("ExecuteStream() error = nil, want 429")
	}
	if got := helps.CodexWeeklyOverdraftStatusSnapshot().Outcomes.UsageLimit; got != before+1 {
		t.Fatalf("usage-limit outcomes = %d, want %d", got, before+1)
	}
}

func TestCodexExecutorWeeklyOverdraftNonStreamInjects(t *testing.T) {
	captured := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read request: %v", errRead)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		captured <- bytes.Clone(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer server.Close()

	cfg := weeklyOverdraftIntegrationConfig(config.CodexWeeklyOverdraftModeInject)
	executor := NewCodexExecutor(&cfg)
	if _, errExecute := executor.Execute(context.Background(), weeklyOverdraftIntegrationAuth(server.URL), weeklyOverdraftIntegrationRequest(), weeklyOverdraftIntegrationOptions()); errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	select {
	case body := <-captured:
		if got := len(gjson.GetBytes(body, "input").Array()); got != 3 {
			t.Fatalf("upstream input items = %d, want 3; body=%s", got, body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for HTTP upstream payload")
	}
}

func TestCodexWebsocketsExecutorWeeklyOverdraftInjectsOnce(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	captured := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade websocket: %v", errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()

		_, body, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Errorf("read websocket request: %v", errRead)
			return
		}
		captured <- bytes.Clone(body)
		completed := []byte(`{"type":"response.completed","response":{"id":"resp_ws_1","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Errorf("write websocket response: %v", errWrite)
		}
	}))
	defer server.Close()

	cfg := weeklyOverdraftIntegrationConfig(config.CodexWeeklyOverdraftModeInject)
	executor := NewCodexWebsocketsExecutor(&cfg)
	if _, errExecute := executor.Execute(context.Background(), weeklyOverdraftIntegrationAuth(server.URL), weeklyOverdraftIntegrationRequest(), weeklyOverdraftIntegrationOptions()); errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	select {
	case body := <-captured:
		if got := gjson.GetBytes(body, "type").String(); got != "response.create" {
			t.Fatalf("websocket type = %q, want response.create", got)
		}
		if got := len(gjson.GetBytes(body, "input").Array()); got != 3 {
			t.Fatalf("upstream input items = %d, want 3; body=%s", got, body)
		}
		callID := gjson.GetBytes(body, "input.1.call_id").String()
		if callID == "" || gjson.GetBytes(body, "input.2.call_id").String() != callID {
			t.Fatalf("weekly overdraft pair is not linked: %s", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for WebSocket upstream payload")
	}
}

func TestCodexWebsocketsExecutorWeeklyOverdraftStreamInjects(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	captured := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade websocket: %v", errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()

		_, body, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Errorf("read websocket request: %v", errRead)
			return
		}
		captured <- bytes.Clone(body)
		completed := []byte(`{"type":"response.completed","response":{"id":"resp_ws_stream_1","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Errorf("write websocket response: %v", errWrite)
		}
	}))
	defer server.Close()

	cfg := weeklyOverdraftIntegrationConfig(config.CodexWeeklyOverdraftModeInject)
	executor := NewCodexWebsocketsExecutor(&cfg)
	result, errExecute := executor.ExecuteStream(context.Background(), weeklyOverdraftIntegrationAuth(server.URL), weeklyOverdraftIntegrationRequest(), weeklyOverdraftIntegrationOptions())
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error = %v", chunk.Err)
		}
	}

	select {
	case body := <-captured:
		if got := len(gjson.GetBytes(body, "input").Array()); got != 3 {
			t.Fatalf("upstream input items = %d, want 3; body=%s", got, body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for WebSocket upstream payload")
	}
}

func TestCodexWebsocketResendReusesPreparedWeeklyOverdraftBody(t *testing.T) {
	cfg := weeklyOverdraftIntegrationConfig(config.CodexWeeklyOverdraftModeInject)
	auth := weeklyOverdraftIntegrationAuth("https://example.test")
	req := weeklyOverdraftIntegrationRequest()
	prepared, decision := helps.ApplyCodexWeeklyOverdraft(helps.CodexWeeklyOverdraftRequest{
		Config:    cfg.Codex.WeeklyOverdraft,
		AuthID:    auth.ID,
		SessionID: helps.ProviderSessionUUID("codex", req.Metadata),
		OAuth:     true,
		Body:      req.Payload,
	})
	if decision.Action != helps.CodexWeeklyOverdraftActionInjected {
		t.Fatalf("decision = %#v", decision)
	}

	first := buildCodexWebsocketRequestBody(prepared)
	second := buildCodexWebsocketRequestBody(prepared)
	if len(gjson.GetBytes(first, "input").Array()) != 3 || len(gjson.GetBytes(second, "input").Array()) != 3 {
		t.Fatalf("resend duplicated weekly overdraft pair: first=%s second=%s", first, second)
	}
}

func weeklyOverdraftIntegrationConfig(mode string) config.Config {
	overdraft := config.DefaultCodexWeeklyOverdraftConfig()
	overdraft.Enabled = true
	overdraft.Mode = mode
	overdraft.CanaryPercent = 100
	return config.Config{
		SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
		Codex:     config.CodexConfig{WeeklyOverdraft: overdraft},
	}
}

func weeklyOverdraftIntegrationAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       "oauth-auth-1",
		Provider: "codex",
		Attributes: map[string]string{
			"base_url": baseURL,
		},
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "test-access-token",
		},
	}
}

func weeklyOverdraftIntegrationRequest() cliproxyexecutor.Request {
	return cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","stream":true,"input":[{"type":"message","role":"user","content":"hello"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "weekly-overdraft-session",
		},
	}
}

func weeklyOverdraftIntegrationOptions() cliproxyexecutor.Options {
	return cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	}
}
