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
			if s.lifecycleController.Status().Mode != lifecycle.ModeActive {
				c.Header("Retry-After", "1")
				c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "lifecycle_read_only", "mode": s.lifecycleController.Status().Mode})
				return
			}
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
		return strings.HasPrefix(path, "/v0/management/plugins/") && strings.HasSuffix(path, "/config")
	}
}

func publicLifecyclePath(path string) bool {
	return path == "/" || path == "/healthz" || path == "/management.html"
}
