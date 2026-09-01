package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestListAuthFilesAggregatesCodexReplicaRuntimeState(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	var occupied *coreauth.CodexReplicaConcurrencyLease
	for index := 1; index <= 2; index++ {
		auth := &coreauth.Auth{
			ID:         "codex.json::replica:" + strconv.Itoa(index),
			Index:      "shared-auth-index",
			FileName:   "codex.json",
			Provider:   "codex",
			Status:     coreauth.StatusActive,
			EgressIPv6: "2001:db8::" + strconv.Itoa(index),
			Success:    int64(index),
			Failed:     int64(index - 1),
			Metadata: map[string]any{
				"type":         "codex",
				"access_token": "test",
				"codex_replica": map[string]any{
					"enabled": true, "count": 2, "concurrency": 10,
				},
			},
			Attributes: map[string]string{
				coreauth.AttributeRuntimeOnly:             "true",
				coreauth.AttributeCodexReplicaGroup:       "codex.json",
				coreauth.AttributeCodexReplicaIndex:       strconv.Itoa(index),
				coreauth.AttributeCodexReplicaCount:       "2",
				coreauth.AttributeCodexReplicaConcurrency: "10",
			},
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register replica %d: %v", index, errRegister)
		}
		if index == 1 {
			var errAcquire error
			occupied, errAcquire = coreauth.AcquireCodexReplicaConcurrency(auth)
			if errAcquire != nil || occupied == nil {
				t.Fatalf("occupy first replica: %v", errAcquire)
			}
		}
	}
	defer occupied.Release()

	handler := &Handler{authManager: manager}
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)
	handler.ListAuthFiles(ctx)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Files []map[string]any `json:"files"`
	}
	if errDecode := json.Unmarshal(response.Body.Bytes(), &payload); errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if len(payload.Files) != 1 {
		t.Fatalf("files = %d, want one physical account: %s", len(payload.Files), response.Body.String())
	}
	entry := payload.Files[0]
	for key, want := range map[string]float64{
		"replica_count":           2,
		"replica_concurrency":     10,
		"replica_total_capacity":  20,
		"replica_active":          1,
		"replica_egress_assigned": 2,
		"success":                 3,
		"failed":                  1,
	} {
		if got := entry[key]; got != want {
			t.Fatalf("%s = %#v, want %v; entry=%#v", key, got, want, entry)
		}
	}
	if entry["replica_enabled"] != true || entry["auth_index"] != "shared-auth-index" || entry["name"] != "codex.json" {
		t.Fatalf("replica identity summary = %#v", entry)
	}
	details, okDetails := entry["replica_concurrency_details"].([]any)
	if !okDetails || len(details) != 2 {
		t.Fatalf("replica concurrency details = %#v, want two entries", entry["replica_concurrency_details"])
	}
	for index, wantActive := range []float64{1, 0} {
		detail, okDetail := details[index].(map[string]any)
		if !okDetail {
			t.Fatalf("replica concurrency detail %d = %#v", index+1, details[index])
		}
		if detail["index"] != float64(index+1) || detail["active"] != wantActive || detail["limit"] != float64(10) || detail["egress_assigned"] != true {
			t.Fatalf("replica concurrency detail %d = %#v", index+1, detail)
		}
	}
}
