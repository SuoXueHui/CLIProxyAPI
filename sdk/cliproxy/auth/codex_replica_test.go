package auth

import (
	"testing"
	"time"
)

func TestNewCodexReplicaSourceClearsRuntimeState(t *testing.T) {
	auth := &Auth{
		ID:          "codex.json::replica:2",
		Index:       "shared-index",
		FileName:    "codex.json",
		Provider:    "codex",
		Status:      StatusError,
		Unavailable: true,
		EgressIPv6:  "2001:db8::2",
		Success:     12,
		Failed:      3,
		Metadata: map[string]any{
			"access_token": "test",
			CodexReplicaMetadataKey: map[string]any{
				"enabled": true, "count": 3, "concurrency": 4,
			},
		},
		Attributes: map[string]string{
			AttributeCodexReplicaGroup:       "codex.json",
			AttributeCodexReplicaIndex:       "2",
			AttributeCodexReplicaCount:       "6",
			AttributeCodexReplicaConcurrency: "10",
			AttributeAuthKind:                AuthKindOAuth,
		},
		ModelStates: map[string]*ModelState{"gpt-5.4": {Status: StatusError}},
	}
	now := time.Now()
	auth.recordRecentRequest(now, true)

	source, ok := NewCodexReplicaSource(auth)
	if !ok || source == nil {
		t.Fatal("NewCodexReplicaSource() did not recognize replica")
	}
	if source.ID != "codex.json" || source.Index != "shared-index" || source.FileName != "codex.json" {
		t.Fatalf("source identity = %#v", source)
	}
	if source.EgressIPv6 != "" || source.Success != 0 || source.Failed != 0 || source.Unavailable || len(source.ModelStates) != 0 {
		t.Fatalf("source retained runtime state: %#v", source)
	}
	for _, bucket := range source.RecentRequestsSnapshot(now) {
		if bucket.Success != 0 || bucket.Failed != 0 {
			t.Fatalf("source retained recent requests: %#v", bucket)
		}
	}
	for _, key := range []string{AttributeCodexReplicaGroup, AttributeCodexReplicaIndex, AttributeCodexReplicaCount, AttributeCodexReplicaConcurrency} {
		if _, exists := source.Attributes[key]; exists {
			t.Fatalf("source retained runtime attribute %q", key)
		}
	}
	if source.Attributes[AttributeAuthKind] != AuthKindOAuth {
		t.Fatalf("source lost auth kind: %#v", source.Attributes)
	}
}
