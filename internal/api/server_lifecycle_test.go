package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/lifecycle"
)

func TestLifecycleMiddlewareGatesProxyAndManagementWrites(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controller := lifecycle.NewController(lifecycle.ModeStandby)
	server := &Server{lifecycleController: controller}
	engine := gin.New()
	engine.Use(server.lifecycleMiddleware())
	engine.GET("/healthz", func(c *gin.Context) { c.Status(http.StatusOK) })
	engine.GET("/v1/models", func(c *gin.Context) { c.Status(http.StatusOK) })
	engine.GET("/v0/management/config", func(c *gin.Context) { c.Status(http.StatusOK) })
	engine.PUT("/v0/management/config", func(c *gin.Context) { c.Status(http.StatusOK) })
	engine.PUT("/v0/management/lifecycle", func(c *gin.Context) { c.Status(http.StatusOK) })
	engine.GET("/v0/management/codex-auth-url", func(c *gin.Context) { c.Status(http.StatusOK) })
	engine.GET("/v0/management/status", func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, tc := range []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "/healthz", http.StatusOK},
		{http.MethodGet, "/v1/models", http.StatusServiceUnavailable},
		{http.MethodGet, "/v0/management/config", http.StatusOK},
		{http.MethodPut, "/v0/management/config", http.StatusServiceUnavailable},
		{http.MethodPut, "/v0/management/lifecycle", http.StatusOK},
		{http.MethodGet, "/v0/management/codex-auth-url", http.StatusServiceUnavailable},
		{http.MethodGet, "/v0/management/status", http.StatusOK},
	} {
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(tc.method, tc.path, nil))
		if recorder.Code != tc.want {
			t.Fatalf("%s %s = %d, want %d", tc.method, tc.path, recorder.Code, tc.want)
		}
	}
}

func TestLifecycleMiddlewareTracksServingReadOnlyRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controller := lifecycle.NewController(lifecycle.ModeServingReadOnly)
	server := &Server{lifecycleController: controller}
	engine := gin.New()
	engine.Use(server.lifecycleMiddleware())
	engine.GET("/v1/models", func(c *gin.Context) {
		if got := controller.Status().ActiveRequests; got != 1 {
			t.Errorf("active requests inside handler = %d, want 1", got)
		}
		c.Status(http.StatusOK)
	})

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /v1/models = %d", recorder.Code)
	}
	if got := controller.Status().ActiveRequests; got != 0 {
		t.Fatalf("active requests after handler = %d, want 0", got)
	}
}
