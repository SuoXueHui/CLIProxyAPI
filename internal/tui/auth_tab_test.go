package tui

import (
	"strings"
	"testing"
)

func TestRenderDetailIncludesEgressIPv6(t *testing.T) {
	model := authTabModel{}
	detail := model.renderDetail(map[string]any{
		"name":        "codex-user@example.com.json",
		"egress_ipv6": "2610:150:805f:f80e:100::42",
	})

	if !strings.Contains(detail, "Egress IPv6") {
		t.Fatalf("expected detail label, got %q", detail)
	}
	if !strings.Contains(detail, "2610:150:805f:f80e:100::42") {
		t.Fatalf("expected detail IPv6 value, got %q", detail)
	}
}
