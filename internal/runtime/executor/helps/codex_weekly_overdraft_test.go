package helps

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func weeklyOverdraftTestConfig() config.CodexWeeklyOverdraftConfig {
	cfg := config.DefaultCodexWeeklyOverdraftConfig()
	cfg.Enabled = true
	cfg.Mode = config.CodexWeeklyOverdraftModeInject
	cfg.CanaryPercent = 100
	return cfg
}

func weeklyOverdraftTestRequest(body []byte) CodexWeeklyOverdraftRequest {
	return CodexWeeklyOverdraftRequest{
		Config:    weeklyOverdraftTestConfig(),
		AuthID:    "auth-1",
		SessionID: "session-1",
		OAuth:     true,
		Body:      body,
	}
}

func TestApplyCodexWeeklyOverdraftDisabledReturnsOriginalBody(t *testing.T) {
	body := []byte(`{"input":[{"type":"message","role":"user","content":"hello"}]}`)
	req := weeklyOverdraftTestRequest(body)
	req.Config.Enabled = false

	got, decision := ApplyCodexWeeklyOverdraft(req)
	assertSameBodyBacking(t, got, body)
	if decision.Reason != CodexWeeklyOverdraftReasonDisabled || decision.Action != CodexWeeklyOverdraftActionSkipped {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestApplyCodexWeeklyOverdraftObserveDoesNotMutate(t *testing.T) {
	body := []byte(`{"input":[{"type":"message","role":"user","content":"hello"}]}`)
	req := weeklyOverdraftTestRequest(body)
	req.Config.Mode = config.CodexWeeklyOverdraftModeObserve

	got, decision := ApplyCodexWeeklyOverdraft(req)
	assertSameBodyBacking(t, got, body)
	if decision.Action != CodexWeeklyOverdraftActionObserved || decision.Tail != CodexWeeklyOverdraftTailUserMessage || decision.PairCount != 1 {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestApplyCodexWeeklyOverdraftOAuthGate(t *testing.T) {
	body := []byte(`{"input":[{"type":"message","role":"user","content":"hello"}]}`)
	req := weeklyOverdraftTestRequest(body)
	req.OAuth = false

	got, decision := ApplyCodexWeeklyOverdraft(req)
	assertSameBodyBacking(t, got, body)
	if decision.Reason != CodexWeeklyOverdraftReasonNonOAuth {
		t.Fatalf("decision = %#v", decision)
	}

	req.Config.OAuthOnly = false
	got, decision = ApplyCodexWeeklyOverdraft(req)
	if gotCount := len(gjson.GetBytes(got, "input").Array()); gotCount != 3 {
		t.Fatalf("input items = %d, want 3", gotCount)
	}
	if decision.Action != CodexWeeklyOverdraftActionInjected {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestApplyCodexWeeklyOverdraftForRequestClassifiesOAuth(t *testing.T) {
	body := []byte(`{"input":[{"type":"message","role":"user","content":"hello"}]}`)
	cfg := &config.Config{Codex: config.CodexConfig{WeeklyOverdraft: weeklyOverdraftTestConfig()}}
	req := cliproxyexecutor.Request{
		Payload: body,
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "session-1",
		},
	}

	oauth := &cliproxyauth.Auth{ID: "oauth-1", Provider: "codex", Metadata: map[string]any{"access_token": "token"}}
	got, decision := ApplyCodexWeeklyOverdraftForRequest(cfg, oauth, req, body)
	if decision.Action != CodexWeeklyOverdraftActionInjected || len(gjson.GetBytes(got, "input").Array()) != 3 {
		t.Fatalf("OAuth decision = %#v body=%s", decision, got)
	}

	apiKey := &cliproxyauth.Auth{ID: "key-1", Provider: "codex", Attributes: map[string]string{"api_key": "key"}}
	got, decision = ApplyCodexWeeklyOverdraftForRequest(cfg, apiKey, req, body)
	assertSameBodyBacking(t, got, body)
	if decision.Reason != CodexWeeklyOverdraftReasonNonOAuth {
		t.Fatalf("API key decision = %#v", decision)
	}

	explicitOAuth := &cliproxyauth.Auth{
		ID:       "oauth-explicit",
		Provider: "codex",
		Attributes: map[string]string{
			cliproxyauth.AttributeAuthKind: cliproxyauth.AuthKindOAuth,
			cliproxyauth.AttributeAPIKey:   "compatibility-token",
		},
	}
	got, decision = ApplyCodexWeeklyOverdraftForRequest(cfg, explicitOAuth, req, body)
	if decision.Action != CodexWeeklyOverdraftActionInjected || len(gjson.GetBytes(got, "input").Array()) != 3 {
		t.Fatalf("explicit OAuth decision = %#v body=%s", decision, got)
	}
}

func TestApplyCodexWeeklyOverdraftEligibleTails(t *testing.T) {
	tests := []struct {
		name string
		body string
		tail string
	}{
		{name: "user message", body: `{"input":[{"type":"message","role":"user","content":"hello"}]}`, tail: CodexWeeklyOverdraftTailUserMessage},
		{name: "function output", body: `{"input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`, tail: CodexWeeklyOverdraftTailFunctionOutput},
		{name: "custom output", body: `{"input":[{"type":"custom_tool_call_output","call_id":"call_2","output":[{"type":"input_text","text":"ok"}]}]}`, tail: CodexWeeklyOverdraftTailCustomToolOutput},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, decision := ApplyCodexWeeklyOverdraft(weeklyOverdraftTestRequest([]byte(test.body)))
			if decision.Action != CodexWeeklyOverdraftActionInjected || decision.Tail != test.tail {
				t.Fatalf("decision = %#v", decision)
			}
			assertMatchedOverdraftPairs(t, got, 1)
		})
	}
}

func TestApplyCodexWeeklyOverdraftUserOnlyRejectsToolOutputTail(t *testing.T) {
	body := []byte(`{"input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`)
	req := weeklyOverdraftTestRequest(body)
	req.Config.TailPolicy = config.CodexWeeklyOverdraftTailUserOnly

	got, decision := ApplyCodexWeeklyOverdraft(req)
	assertSameBodyBacking(t, got, body)
	if decision.Reason != CodexWeeklyOverdraftReasonUnsupportedTail {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestApplyCodexWeeklyOverdraftRejectsMalformedOversizedAndUnsupportedBodies(t *testing.T) {
	tests := []struct {
		name   string
		body   []byte
		mutate func(*CodexWeeklyOverdraftRequest)
		reason string
	}{
		{name: "invalid json", body: []byte(`{"input":`), reason: CodexWeeklyOverdraftReasonMalformed},
		{name: "missing input", body: []byte(`{"model":"gpt-5.4"}`), reason: CodexWeeklyOverdraftReasonMalformed},
		{name: "empty input", body: []byte(`{"input":[]}`), reason: CodexWeeklyOverdraftReasonMalformed},
		{name: "assistant tail", body: []byte(`{"input":[{"type":"message","role":"assistant","content":"hello"}]}`), reason: CodexWeeklyOverdraftReasonUnsupportedTail},
		{name: "oversized", body: []byte(`{"input":[{"type":"message","role":"user","content":"hello"}]}`), mutate: func(req *CodexWeeklyOverdraftRequest) { req.Config.MaxBodyBytes = 8 }, reason: CodexWeeklyOverdraftReasonOversize},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := weeklyOverdraftTestRequest(test.body)
			if test.mutate != nil {
				test.mutate(&req)
			}
			got, decision := ApplyCodexWeeklyOverdraft(req)
			assertSameBodyBacking(t, got, test.body)
			if decision.Reason != test.reason {
				t.Fatalf("decision = %#v, want reason %q", decision, test.reason)
			}
		})
	}
}

func TestApplyCodexWeeklyOverdraftStableCanary(t *testing.T) {
	body := []byte(`{"input":[{"type":"message","role":"user","content":"hello"}]}`)
	selectedSession := ""
	skippedSession := ""
	for i := 0; i < 10000 && (selectedSession == "" || skippedSession == ""); i++ {
		session := fmt.Sprintf("session-%d", i)
		if codexWeeklyOverdraftBucket("auth-1", session) < 10 {
			selectedSession = session
		} else {
			skippedSession = session
		}
	}
	if selectedSession == "" || skippedSession == "" {
		t.Fatal("failed to find deterministic canary fixtures")
	}

	req := weeklyOverdraftTestRequest(body)
	req.Config.CanaryPercent = 10
	req.SessionID = selectedSession
	first, firstDecision := ApplyCodexWeeklyOverdraft(req)
	second, secondDecision := ApplyCodexWeeklyOverdraft(req)
	if firstDecision.Action != CodexWeeklyOverdraftActionInjected || secondDecision.Action != CodexWeeklyOverdraftActionInjected || string(first) != string(second) {
		t.Fatalf("selected decisions = %#v / %#v", firstDecision, secondDecision)
	}

	req.SessionID = skippedSession
	got, decision := ApplyCodexWeeklyOverdraft(req)
	assertSameBodyBacking(t, got, body)
	if decision.Reason != CodexWeeklyOverdraftReasonNonCanary {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestApplyCodexWeeklyOverdraftPairCounts(t *testing.T) {
	body := []byte(`{"input":[{"type":"message","role":"user","content":"hello"}]}`)
	for _, pairCount := range []int{1, 2, 4} {
		t.Run(fmt.Sprintf("pairs-%d", pairCount), func(t *testing.T) {
			req := weeklyOverdraftTestRequest(body)
			req.Config.PairCount = pairCount
			got, decision := ApplyCodexWeeklyOverdraft(req)
			if decision.PairCount != pairCount {
				t.Fatalf("PairCount = %d, want %d", decision.PairCount, pairCount)
			}
			assertMatchedOverdraftPairs(t, got, pairCount)
		})
	}
}

func TestApplyCodexWeeklyOverdraftIsIdempotentForCoreAndPluginMarkers(t *testing.T) {
	tests := []string{"call_cpa_core_overdraft_existing", "call_cpa_overdraft_existing"}
	for _, callID := range tests {
		body := []byte(fmt.Sprintf(`{"input":[{"type":"message","role":"user","content":"hello"},{"type":"custom_tool_call","name":"exec","call_id":%q,"input":"noop"},{"type":"custom_tool_call_output","call_id":%q,"output":[]}]}`, callID, callID))
		got, decision := ApplyCodexWeeklyOverdraft(weeklyOverdraftTestRequest(body))
		assertSameBodyBacking(t, got, body)
		if decision.Reason != CodexWeeklyOverdraftReasonAlreadyInjected {
			t.Fatalf("callID %q decision = %#v", callID, decision)
		}
	}
}

func TestRecordCodexWeeklyOverdraftOutcome(t *testing.T) {
	resetCodexWeeklyOverdraftStatusForTest()
	decision := CodexWeeklyOverdraftDecision{Action: CodexWeeklyOverdraftActionInjected}

	RecordCodexWeeklyOverdraftOutcome(decision, nil)
	RecordCodexWeeklyOverdraftOutcome(decision, weeklyOverdraftStatusError{status: http.StatusTooManyRequests})
	RecordCodexWeeklyOverdraftOutcome(decision, weeklyOverdraftStatusError{status: http.StatusUnauthorized})
	RecordCodexWeeklyOverdraftOutcome(decision, context.Canceled)
	RecordCodexWeeklyOverdraftOutcome(decision, errors.New("boom"))
	RecordCodexWeeklyOverdraftOutcome(CodexWeeklyOverdraftDecision{Action: CodexWeeklyOverdraftActionSkipped}, nil)

	got := CodexWeeklyOverdraftStatusSnapshot()
	if got.Outcomes.Success != 1 || got.Outcomes.UsageLimit != 1 || got.Outcomes.HardStop != 1 || got.Outcomes.Canceled != 1 || got.Outcomes.OtherFailure != 1 {
		t.Fatalf("Outcomes = %#v", got.Outcomes)
	}
}

func TestCodexWeeklyOverdraftStatusCountsDecisions(t *testing.T) {
	resetCodexWeeklyOverdraftStatusForTest()
	body := []byte(`{"input":[{"type":"message","role":"user","content":"hello"}]}`)

	disabledReq := weeklyOverdraftTestRequest(body)
	disabledReq.Config.Enabled = false
	ApplyCodexWeeklyOverdraft(disabledReq)

	observeReq := weeklyOverdraftTestRequest(body)
	observeReq.Config.Mode = config.CodexWeeklyOverdraftModeObserve
	ApplyCodexWeeklyOverdraft(observeReq)

	ApplyCodexWeeklyOverdraft(weeklyOverdraftTestRequest(body))

	got := CodexWeeklyOverdraftStatusSnapshot()
	if got.Evaluated != 3 || got.Skipped[CodexWeeklyOverdraftReasonDisabled] != 1 || got.Observed != 1 || got.Injected != 1 {
		t.Fatalf("snapshot = %#v", got)
	}
}

func assertSameBodyBacking(t *testing.T, got, want []byte) {
	t.Helper()
	if len(got) != len(want) || string(got) != string(want) {
		t.Fatalf("body changed: got %q want %q", got, want)
	}
	if len(want) > 0 && &got[0] != &want[0] {
		t.Fatal("skip path returned a copied body")
	}
}

func assertMatchedOverdraftPairs(t *testing.T, body []byte, pairCount int) {
	t.Helper()
	items := gjson.GetBytes(body, "input").Array()
	if got, want := len(items), 1+pairCount*2; got != want {
		t.Fatalf("input items = %d, want %d; body=%s", got, want, body)
	}
	seen := make(map[string]bool, pairCount)
	for i := 0; i < pairCount; i++ {
		call := items[1+i*2]
		output := items[2+i*2]
		callID := call.Get("call_id").String()
		if call.Get("type").String() != "custom_tool_call" || call.Get("name").String() != "exec" || callID == "" {
			t.Fatalf("invalid call item: %s", call.Raw)
		}
		if got := call.Get("input").String(); !strings.Contains(got, `tools.exec_command({"cmd":"true"`) || strings.Contains(got, `\{"cmd"`) {
			t.Fatalf("invalid exec input %q", got)
		}
		if output.Get("type").String() != "custom_tool_call_output" || output.Get("call_id").String() != callID {
			t.Fatalf("invalid output item: %s", output.Raw)
		}
		if seen[callID] {
			t.Fatalf("duplicate call ID %q", callID)
		}
		seen[callID] = true
	}
}

type weeklyOverdraftStatusError struct {
	status int
}

func (e weeklyOverdraftStatusError) Error() string   { return http.StatusText(e.status) }
func (e weeklyOverdraftStatusError) StatusCode() int { return e.status }

var _ cliproxyexecutor.StatusError = weeklyOverdraftStatusError{}
