package helps

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func BenchmarkApplyCodexWeeklyOverdraftDisabled(b *testing.B) {
	body := []byte(`{"input":[{"type":"message","role":"user","content":"hello"}]}`)
	req := CodexWeeklyOverdraftRequest{
		Config:    config.DefaultCodexWeeklyOverdraftConfig(),
		AuthID:    "auth-1",
		SessionID: "session-1",
		OAuth:     true,
		Body:      body,
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		got, _ := ApplyCodexWeeklyOverdraft(req)
		if len(got) != len(body) {
			b.Fatal("body changed")
		}
	}
}
