package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
)

// GetCodexWeeklyOverdraftStatus returns the effective experiment config plus
// process-local counters. Account rows expose only management-visible auth IDs
// and never include request payloads, sessions, tokens, or credential content.
func (h *Handler) GetCodexWeeklyOverdraftStatus(c *gin.Context) {
	overdraft := config.DefaultCodexWeeklyOverdraftConfig()
	if h != nil {
		h.mu.Lock()
		if h.cfg != nil {
			overdraft = h.cfg.Codex.WeeklyOverdraft
		}
		h.mu.Unlock()
	}
	c.JSON(http.StatusOK, gin.H{
		"config": overdraft,
		"status": helps.CodexWeeklyOverdraftStatusSnapshot(c.QueryArray("auth-id")...),
	})
}
