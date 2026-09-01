package api

import (
	"context"
	"errors"
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

func TestLifecycleMiddlewareTracksManagementWritesDuringDrain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controller := lifecycle.NewController(lifecycle.ModeActive)
	server := &Server{lifecycleController: controller}
	engine := gin.New()
	engine.Use(server.lifecycleMiddleware())
	started := make(chan struct{})
	release := make(chan struct{})
	engine.PUT("/v0/management/config.yaml", func(c *gin.Context) {
		close(started)
		<-release
		c.Status(http.StatusOK)
	})

	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, "/v0/management/config.yaml", nil))
		if recorder.Code != http.StatusOK {
			t.Errorf("management write status = %d, want %d", recorder.Code, http.StatusOK)
		}
	}()
	<-started
	if got := controller.Status().ActiveRequests; got != 1 {
		t.Fatalf("active management writes = %d, want 1", got)
	}
	if _, errDrain := controller.Transition(context.Background(), lifecycle.ModeDraining, 0); errDrain != nil {
		t.Fatalf("active -> draining error = %v", errDrain)
	}
	if _, errStandby := controller.Transition(context.Background(), lifecycle.ModeStandby, 0); !errors.Is(errStandby, lifecycle.ErrActiveRequests) {
		t.Fatalf("draining -> standby error = %v, want active requests", errStandby)
	}
	close(release)
	<-requestDone
	if _, errStandby := controller.Transition(context.Background(), lifecycle.ModeStandby, 0); errStandby != nil {
		t.Fatalf("draining -> standby after management write error = %v", errStandby)
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
