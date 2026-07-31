package helps

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type utlsClientRoundTripFunc func(*http.Request) (*http.Response, error)

func (f utlsClientRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestNewUtlsHTTPClientUsesContextRoundTripperForProtectedHost(t *testing.T) {
	t.Parallel()

	called := false
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", utlsClientRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		called = true
		if req.URL.Hostname() != "chatgpt.com" {
			t.Fatalf("hostname = %q, want chatgpt.com", req.URL.Hostname())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("{}")),
			Request:    req,
		}, nil
	}))

	client := NewUtlsHTTPClient(ctx, nil, nil, 0)
	resp, err := client.Get("https://chatgpt.com/backend-api/codex/responses")
	if err != nil {
		t.Fatalf("client.Get returned error: %v", err)
	}
	if errClose := resp.Body.Close(); errClose != nil {
		t.Fatalf("response body close returned error: %v", errClose)
	}
	if !called {
		t.Fatal("expected context RoundTripper to handle protected host request")
	}
}

func TestNewUtlsHTTPClientReusesUTLSTransportForSameProxy(t *testing.T) {
	first := NewUtlsHTTPClient(context.Background(), nil, nil, 0)
	second := NewUtlsHTTPClient(context.Background(), nil, nil, 0)

	firstFallback, ok := first.Transport.(*fallbackRoundTripper)
	if !ok {
		t.Fatalf("first transport type = %T, want *fallbackRoundTripper", first.Transport)
	}
	secondFallback, ok := second.Transport.(*fallbackRoundTripper)
	if !ok {
		t.Fatalf("second transport type = %T, want *fallbackRoundTripper", second.Transport)
	}
	if firstFallback.utls != secondFallback.utls {
		t.Fatal("protected-host transport was not reused")
	}
}

func TestTrackedResponseBodyReleasesConnectionOnce(t *testing.T) {
	released := 0
	body := &trackedResponseBody{
		ReadCloser: io.NopCloser(strings.NewReader("ok")),
		release: func() {
			released++
		},
	}

	if _, err := io.ReadAll(body); err != nil {
		t.Fatalf("io.ReadAll returned error: %v", err)
	}
	if err := body.Close(); err != nil {
		t.Fatalf("response body close returned error: %v", err)
	}
	if released != 1 {
		t.Fatalf("connection release count = %d, want 1", released)
	}
}
