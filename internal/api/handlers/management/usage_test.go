package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
)

func TestGetUsageQueuePopsRequestedRecords(t *testing.T) {
	withManagementUsageQueue(t, func() {
		redisqueue.Enqueue([]byte(`{"id":1}`))
		redisqueue.Enqueue([]byte(`{"id":2}`))
		redisqueue.Enqueue([]byte(`{"id":3}`))

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage-queue?count=2", nil)

		h := &Handler{}
		h.GetUsageQueue(ginCtx)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}

		var payload []json.RawMessage
		if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
			t.Fatalf("unmarshal response: %v", errUnmarshal)
		}
		if len(payload) != 2 {
			t.Fatalf("response records = %d, want 2", len(payload))
		}
		requireRecordID(t, payload[0], 1)
		requireRecordID(t, payload[1], 2)

		remaining := redisqueue.PopOldest(10)
		if len(remaining) != 1 || string(remaining[0]) != `{"id":3}` {
			t.Fatalf("remaining queue = %q, want third item only", remaining)
		}
	})
}

func TestUsageQueueClaimAndAckHTTP(t *testing.T) {
	if errConfigure := redisqueue.ConfigureOutbox(filepath.Join(t.TempDir(), "usage-outbox.sqlite")); errConfigure != nil {
		t.Fatalf("ConfigureOutbox() error = %v", errConfigure)
	}
	redisqueue.SetEnabled(true)
	t.Cleanup(func() {
		redisqueue.SetEnabled(false)
		_ = redisqueue.ConfigureOutbox("disabled")
	})
	redisqueue.Enqueue([]byte(`{"id":1}`))
	redisqueue.Enqueue([]byte(`{"id":2}`))

	h := &Handler{}
	claimRec := httptest.NewRecorder()
	claimCtx, _ := gin.CreateTestContext(claimRec)
	claimCtx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/usage-queue/claim", bytes.NewBufferString(`{"count":2,"lease_seconds":60}`))
	claimCtx.Request.Header.Set("Content-Type", "application/json")
	h.ClaimUsageQueue(claimCtx)
	if claimRec.Code != http.StatusOK {
		t.Fatalf("claim status = %d, want 200 body=%s", claimRec.Code, claimRec.Body.String())
	}
	var claim struct {
		LeaseID string `json:"lease_id"`
		Items   []struct {
			DeliveryID string          `json:"delivery_id"`
			Payload    json.RawMessage `json:"payload"`
		} `json:"items"`
	}
	if errUnmarshal := json.Unmarshal(claimRec.Body.Bytes(), &claim); errUnmarshal != nil {
		t.Fatalf("unmarshal claim: %v", errUnmarshal)
	}
	if claim.LeaseID == "" || len(claim.Items) != 2 {
		t.Fatalf("claim response = %+v, want token and two items", claim)
	}
	requireRecordID(t, claim.Items[0].Payload, 1)

	ackBody, _ := json.Marshal(map[string]any{
		"lease_id":     claim.LeaseID,
		"delivery_ids": []string{claim.Items[0].DeliveryID},
	})
	ackRec := httptest.NewRecorder()
	ackCtx, _ := gin.CreateTestContext(ackRec)
	ackCtx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/usage-queue/ack", bytes.NewReader(ackBody))
	ackCtx.Request.Header.Set("Content-Type", "application/json")
	h.AckUsageQueue(ackCtx)
	if ackRec.Code != http.StatusOK || !bytes.Contains(ackRec.Body.Bytes(), []byte(`"acked":1`)) {
		t.Fatalf("ack response status=%d body=%s, want acked=1", ackRec.Code, ackRec.Body.String())
	}

	statusRec := httptest.NewRecorder()
	statusCtx, _ := gin.CreateTestContext(statusRec)
	statusCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage-queue/status", nil)
	h.GetUsageQueueStatus(statusCtx)
	if statusRec.Code != http.StatusOK || !bytes.Contains(statusRec.Body.Bytes(), []byte(`"inflight":1`)) {
		t.Fatalf("status response status=%d body=%s, want one inflight", statusRec.Code, statusRec.Body.String())
	}

	// Verify the unacked item is not destructively returned before its lease expires.
	if retry, errRetry := redisqueue.Claim(2, time.Minute); errRetry != nil || len(retry.Items) != 0 {
		t.Fatalf("immediate retry claim = %+v, err=%v, want empty", retry, errRetry)
	}
}

func TestGetUsageQueueInvalidCountDoesNotPop(t *testing.T) {
	withManagementUsageQueue(t, func() {
		redisqueue.Enqueue([]byte(`{"id":1}`))

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage-queue?count=0", nil)

		h := &Handler{}
		h.GetUsageQueue(ginCtx)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
		}

		remaining := redisqueue.PopOldest(10)
		if len(remaining) != 1 || string(remaining[0]) != `{"id":1}` {
			t.Fatalf("remaining queue = %q, want original item", remaining)
		}
	})
}

func withManagementUsageQueue(t *testing.T, fn func()) {
	t.Helper()

	prevQueueEnabled := redisqueue.Enabled()
	redisqueue.SetEnabled(false)
	redisqueue.SetEnabled(true)

	defer func() {
		redisqueue.SetEnabled(false)
		redisqueue.SetEnabled(prevQueueEnabled)
	}()

	fn()
}

func requireRecordID(t *testing.T, raw json.RawMessage, want int) {
	t.Helper()

	var payload struct {
		ID int `json:"id"`
	}
	if errUnmarshal := json.Unmarshal(raw, &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal record: %v", errUnmarshal)
	}
	if payload.ID != want {
		t.Fatalf("record id = %d, want %d", payload.ID, want)
	}
}
