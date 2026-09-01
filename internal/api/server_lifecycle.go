package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/lifecycle"
)

func (s *Server) lifecycleMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s == nil || s.lifecycleController == nil || lifecycleControlPath(c.Request.URL.Path) {
			c.Next()
			return
		}
		if managementReadPath(c.Request.Method, c.Request.URL.Path) || publicLifecyclePath(c.Request.URL.Path) {
			c.Next()
			return
		}
		if strings.HasPrefix(c.Request.URL.Path, "/v0/management") || strings.HasPrefix(c.Request.URL.Path, "/v0/resource/plugins/") {
			done, admitted := s.lifecycleController.AdmitProxy()
			if !admitted || s.lifecycleController.Status().Mode != lifecycle.ModeActive {
				if admitted {
					done()
				}
				c.Header("Retry-After", "1")
				c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "lifecycle_read_only", "mode": s.lifecycleController.Status().Mode})
				return
			}
			defer done()
			c.Next()
			return
		}
		done, admitted := s.lifecycleController.AdmitProxy()
		if !admitted {
			c.Header("Retry-After", "1")
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "lifecycle_not_accepting", "mode": s.lifecycleController.Status().Mode})
			return
		}
		defer done()
		c.Next()
	}
}

func lifecycleControlPath(path string) bool {
	return path == "/v0/management/lifecycle"
}

func managementReadPath(method, path string) bool {
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	// These GET routes create or cancel OAuth sessions and therefore remain
	// mutation-like even though their HTTP verb is GET.
	switch path {
	case "/v0/management/anthropic-auth-url",
		"/v0/management/codex-auth-url",
		"/v0/management/antigravity-auth-url",
		"/v0/management/kimi-auth-url",
		"/v0/management/xai-auth-url",
		"/v0/management/oauth-callback",
		"/v0/management/oauth-session":
		return false
	}
	switch path {
	case "/v0/management/config",
		"/v0/management/config.yaml",
		"/v0/management/plugins",
		"/v0/management/auth-files",
		"/v0/management/auth-files/models",
		"/v0/management/auth-files/download",
		"/v0/management/usage-queue/status",
		"/v0/management/codex-weekly-overdraft":
		return true
	default:
		return strings.HasPrefix(path, "/v0/management") || strings.HasPrefix(path, "/v0/resource/plugins/")
	}
}

func publicLifecyclePath(path string) bool {
	return path == "/" || path == "/healthz" || path == "/management.html"
}
