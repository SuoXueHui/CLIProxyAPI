package config

import "testing"

func TestParseConfigBytesPreservesUsageOutboxPath(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte("usage-outbox-path: /var/lib/cliproxy/usage.sqlite\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if cfg.UsageOutboxPath != "/var/lib/cliproxy/usage.sqlite" {
		t.Fatalf("UsageOutboxPath = %q, want configured path", cfg.UsageOutboxPath)
	}
}
