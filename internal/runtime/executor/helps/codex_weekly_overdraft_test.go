package helps

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

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

func resetCodexWeeklyOverdraftGatesForTest() {
	codexWeeklyOverdraftGates = sync.Map{}
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

func TestApplyCodexWeeklyOverdraftProbeBypassesInjection(t *testing.T) {
	body := []byte(`{"input":[{"type":"message","role":"user","content":"hello"}]}`)
	cfg := &config.Config{Codex: config.CodexConfig{WeeklyOverdraft: weeklyOverdraftTestConfig()}}
	req := cliproxyexecutor.Request{Payload: body, Metadata: map[string]any{CodexWeeklyOverdraftProbeMetadataKey: true}}
	auth := &cliproxyauth.Auth{ID: "probe-auth", Provider: "codex", Attributes: map[string]string{cliproxyauth.AttributeAuthKind: cliproxyauth.AuthKindOAuth}}
	got, decision := ApplyCodexWeeklyOverdraftForRequest(cfg, auth, req, body)
	assertSameBodyBacking(t, got, body)
	if decision.Action != CodexWeeklyOverdraftActionProbe || decision.Reason != CodexWeeklyOverdraftReasonProbe {
		t.Fatalf("probe decision = %#v", decision)
	}
}

func TestCodexWeeklyOverdraftGateOpensOnlyForDefiniteQuota(t *testing.T) {
	cfg := weeklyOverdraftTestConfig()
	cfg.GateMode = config.CodexWeeklyOverdraftGateModeHeaderOr429
	req := CodexWeeklyOverdraftRequest{Config: cfg, AuthID: "gate-auth", SessionID: "gate-session", OAuth: true, Metadata: map[string]any{"codex_overdraft_window": "5h"}, Body: []byte(`{"input":[{"type":"message","role":"user","content":"hello"}]}`)}
	if _, decision := ApplyCodexWeeklyOverdraftForRequest(&config.Config{Codex: config.CodexConfig{WeeklyOverdraft: cfg}}, &cliproxyauth.Auth{ID: "gate-auth", Provider: "codex", Attributes: map[string]string{cliproxyauth.AttributeAuthKind: cliproxyauth.AuthKindOAuth}}, cliproxyexecutor.Request{Payload: req.Body, Metadata: req.Metadata}, req.Body); decision.Reason != CodexWeeklyOverdraftReasonGateClosed {
		t.Fatalf("initial gate decision = %#v", decision)
	}
	RecordCodexWeeklyOverdraftQuotaEvidence("gate-auth", "5h", http.StatusTooManyRequests, nil, []byte(`{"error":{"code":"usage_limit_reached"}}`))
	_, decision := ApplyCodexWeeklyOverdraftForRequest(&config.Config{Codex: config.CodexConfig{WeeklyOverdraft: cfg}}, &cliproxyauth.Auth{ID: "gate-auth", Provider: "codex", Attributes: map[string]string{cliproxyauth.AttributeAuthKind: cliproxyauth.AuthKindOAuth}}, cliproxyexecutor.Request{Payload: req.Body, Metadata: req.Metadata}, req.Body)
	if decision.Action != CodexWeeklyOverdraftActionInjected {
		t.Fatalf("opened gate decision = %#v", decision)
	}
}

func TestCodexWeeklyOverdraftGatePropagatesCycleEvidence(t *testing.T) {
	cfg := weeklyOverdraftTestConfig()
	cfg.GateMode = config.CodexWeeklyOverdraftGateModeHeaderOr429
	body := []byte(`{"input":[{"type":"message","role":"user","content":"hello"}]}`)
	metadata := map[string]any{"codex_overdraft_window": "7d"}
	RecordCodexWeeklyOverdraftQuotaEvidenceWithThresholdAndCycle("cycle-auth", "7d", 95, "reset:123", time.Now().Add(time.Hour).UnixMilli(), http.StatusTooManyRequests, nil, []byte(`{"error":{"code":"quota_snapshot_threshold"}}`))
	_, decision := ApplyCodexWeeklyOverdraftForRequest(&config.Config{Codex: config.CodexConfig{WeeklyOverdraft: cfg}}, &cliproxyauth.Auth{ID: "cycle-auth", Provider: "codex", Attributes: map[string]string{cliproxyauth.AttributeAuthKind: cliproxyauth.AuthKindOAuth}}, cliproxyexecutor.Request{Payload: body, Metadata: metadata}, body)
	if decision.GateWindow != "7d" || decision.CycleKey != "reset:123" {
		t.Fatalf("gate evidence was not propagated: %#v", decision)
	}
}

func TestCodexWeeklyOverdraftGatePrunesExpiredEvidenceAcrossAccounts(t *testing.T) {
	resetCodexWeeklyOverdraftGatesForTest()
	t.Cleanup(resetCodexWeeklyOverdraftGatesForTest)

	staleKey := "stale-auth\x005h"
	codexWeeklyOverdraftGates.Store(staleKey, codexWeeklyOverdraftGateEvidence{openedAt: time.Now().Add(-7 * time.Hour)})
	RecordCodexWeeklyOverdraftQuotaEvidence("fresh-auth", "5h", http.StatusTooManyRequests, nil, []byte(`{"error":{"code":"usage_limit_reached"}}`))

	if _, staleExists := codexWeeklyOverdraftGates.Load(staleKey); staleExists {
		t.Fatal("expired gate evidence from another auth was not pruned")
	}
	if _, freshExists := codexWeeklyOverdraftGates.Load("fresh-auth\x005h"); !freshExists {
		t.Fatal("fresh gate evidence was not retained")
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

func TestCodexWeeklyOverdraftAccountStatusSeparatesObserveAndInject(t *testing.T) {
	resetCodexWeeklyOverdraftStatusForTest()
	body := []byte(`{"input":[{"type":"message","role":"user","content":"hello"}]}`)

	observeReq := weeklyOverdraftTestRequest(body)
	observeReq.AuthID = "auth-account-a"
	observeReq.Config.Mode = config.CodexWeeklyOverdraftModeObserve
	_, observeDecision := ApplyCodexWeeklyOverdraft(observeReq)
	RecordCodexWeeklyOverdraftOutcome(observeDecision, nil)

	injectReq := weeklyOverdraftTestRequest(body)
	injectReq.AuthID = "auth-account-a"
	_, injectDecision := ApplyCodexWeeklyOverdraft(injectReq)
	RecordCodexWeeklyOverdraftOutcome(injectDecision, weeklyOverdraftStatusError{status: http.StatusTooManyRequests})

	otherReq := weeklyOverdraftTestRequest(body)
	otherReq.AuthID = "auth-account-b"
	_, otherDecision := ApplyCodexWeeklyOverdraft(otherReq)
	RecordCodexWeeklyOverdraftOutcome(otherDecision, nil)

	got := CodexWeeklyOverdraftStatusSnapshot()
	if got.AccountRetentionSeconds != int64((6*time.Hour)/time.Second) {
		t.Fatalf("AccountRetentionSeconds = %d", got.AccountRetentionSeconds)
	}
	accountA := weeklyOverdraftAccountByID(t, got.Accounts, "auth-account-a")
	if accountA.Observed.Requests != 1 || accountA.Observed.Outcomes.Success != 1 {
		t.Fatalf("observed account status = %#v", accountA.Observed)
	}
	if accountA.Injected.Requests != 1 || accountA.Injected.Outcomes.UsageLimit != 1 || accountA.Injected.Outcomes.Success != 0 {
		t.Fatalf("injected account status = %#v", accountA.Injected)
	}
	if accountA.FirstSeenAt.IsZero() || accountA.LastSeenAt.Before(accountA.FirstSeenAt) {
		t.Fatalf("account timestamps = first %s last %s", accountA.FirstSeenAt, accountA.LastSeenAt)
	}
	accountB := weeklyOverdraftAccountByID(t, got.Accounts, "auth-account-b")
	if accountB.Injected.Requests != 1 || accountB.Injected.Outcomes.Success != 1 {
		t.Fatalf("second account status = %#v", accountB.Injected)
	}
}

func TestCodexWeeklyOverdraftAccountStatusExpiresAfterSixHoursOfInactivity(t *testing.T) {
	resetCodexWeeklyOverdraftStatusForTest()
	body := []byte(`{"input":[{"type":"message","role":"user","content":"hello"}]}`)
	req := weeklyOverdraftTestRequest(body)
	req.AuthID = "auth-expiring"
	_, decision := ApplyCodexWeeklyOverdraft(req)
	RecordCodexWeeklyOverdraftOutcome(decision, nil)

	active := codexWeeklyOverdraftStatusSnapshotAt(time.Now().UTC().Add(6*time.Hour-time.Second), nil)
	weeklyOverdraftAccountByID(t, active.Accounts, req.AuthID)

	expired := codexWeeklyOverdraftStatusSnapshotAt(time.Now().UTC().Add(6*time.Hour+time.Second), nil)
	if len(expired.Accounts) != 0 {
		t.Fatalf("expired accounts = %#v", expired.Accounts)
	}
}

func TestCodexWeeklyOverdraftAccountStatusFiltersRequestedAuthIDs(t *testing.T) {
	resetCodexWeeklyOverdraftStatusForTest()
	body := []byte(`{"input":[{"type":"message","role":"user","content":"hello"}]}`)
	for _, authID := range []string{"auth-filter-a", "auth-filter-b"} {
		req := weeklyOverdraftTestRequest(body)
		req.AuthID = authID
		_, decision := ApplyCodexWeeklyOverdraft(req)
		RecordCodexWeeklyOverdraftOutcome(decision, nil)
	}

	got := CodexWeeklyOverdraftStatusSnapshot(" auth-filter-b ", "", "auth-filter-b")
	if len(got.Accounts) != 1 || got.Accounts[0].AuthID != "auth-filter-b" {
		t.Fatalf("filtered accounts = %#v", got.Accounts)
	}
}

func TestCodexWeeklyOverdraftAccountStatusHandlesConcurrentRequests(t *testing.T) {
	resetCodexWeeklyOverdraftStatusForTest()
	body := []byte(`{"input":[{"type":"message","role":"user","content":"hello"}]}`)
	const requests = 100

	var wg sync.WaitGroup
	wg.Add(requests)
	for index := 0; index < requests; index++ {
		go func() {
			defer wg.Done()
			req := weeklyOverdraftTestRequest(body)
			req.AuthID = "auth-concurrent"
			_, decision := ApplyCodexWeeklyOverdraft(req)
			RecordCodexWeeklyOverdraftOutcome(decision, nil)
		}()
	}
	wg.Wait()

	account := weeklyOverdraftAccountByID(t, CodexWeeklyOverdraftStatusSnapshot("auth-concurrent").Accounts, "auth-concurrent")
	if account.Injected.Requests != requests || account.Injected.Outcomes.Success != requests {
		t.Fatalf("concurrent account status = %#v", account.Injected)
	}
}

func weeklyOverdraftAccountByID(t *testing.T, accounts []CodexWeeklyOverdraftAccountStatus, authID string) CodexWeeklyOverdraftAccountStatus {
	t.Helper()
	for _, account := range accounts {
		if account.AuthID == authID {
			return account
		}
	}
	t.Fatalf("account %q not found in %#v", authID, accounts)
	return CodexWeeklyOverdraftAccountStatus{}
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
