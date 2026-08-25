package helps

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
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

func TestNewUtlsHTTPClientSourceIPv6UsesBoundDialer(t *testing.T) {
	t.Parallel()

	client := NewUtlsHTTPClient(context.Background(), nil, &cliproxyauth.Auth{EgressIPv6: "2001:db8::10"}, 0)
	fallback, ok := client.Transport.(*fallbackRoundTripper)
	if !ok {
		t.Fatalf("transport type = %T, want *fallbackRoundTripper", client.Transport)
	}
	utlsRT, ok := fallback.utls.(*utlsRoundTripper)
	if !ok {
		t.Fatalf("uTLS round tripper type = %T, want *utlsRoundTripper", fallback.utls)
	}
	netDialer, ok := utlsRT.dialer.(*net.Dialer)
	if !ok {
		t.Fatalf("uTLS dialer type = %T, want *net.Dialer", utlsRT.dialer)
	}
	localAddr, ok := netDialer.LocalAddr.(*net.TCPAddr)
	if !ok || localAddr == nil || !localAddr.IP.Equal(net.ParseIP("2001:db8::10")) {
		t.Fatalf("uTLS local address = %#v, want 2001:db8::10", netDialer.LocalAddr)
	}
}
