package helps

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	tls "github.com/refraction-networking/utls"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/http2"
	"golang.org/x/net/proxy"
)

const (
	// Keep idle HTTP/2 connections long enough for normal request bursts while
	// bounding the amount of retained upstream state during quiet periods.
	utlsIdleConnectionTimeout = 90 * time.Second
	utlsReaperInterval        = 30 * time.Second
)

// utlsClientConnection tracks users of one HTTP/2 connection. A response body
// can outlive RoundTrip, so the connection must not be reaped until the body
// is fully consumed or closed.
type utlsClientConnection struct {
	conn    *http2.ClientConn
	mu      sync.Mutex
	refs    int
	retired bool
}

func (c *utlsClientConnection) acquire() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.retired {
		return false
	}
	c.refs++
	return true
}

func (c *utlsClientConnection) release() {
	c.mu.Lock()
	if c.refs > 0 {
		c.refs--
	}
	shouldClose := c.retired && c.refs == 0
	conn := c.conn
	c.mu.Unlock()
	if shouldClose {
		_ = conn.Close()
	}
}

func (c *utlsClientConnection) retire() {
	c.mu.Lock()
	c.retired = true
	shouldClose := c.refs == 0
	conn := c.conn
	c.mu.Unlock()
	if shouldClose {
		_ = conn.Close()
	}
}

func (c *utlsClientConnection) isIdle(now time.Time) bool {
	c.mu.Lock()
	if c.retired || c.refs != 0 {
		c.mu.Unlock()
		return false
	}
	c.mu.Unlock()

	state := c.conn.State()
	if state.Closed || state.Closing {
		return true
	}
	if state.StreamsActive != 0 || state.StreamsReserved != 0 || state.StreamsPending != 0 || state.LastIdle.IsZero() {
		return false
	}
	return now.Sub(state.LastIdle) >= utlsIdleConnectionTimeout
}

// utlsRoundTripper implements http.RoundTripper using utls with Chrome fingerprint
// to bypass Cloudflare's TLS fingerprinting on Anthropic domains.
type utlsRoundTripper struct {
	mu          sync.Mutex
	connections map[string]*utlsClientConnection
	pending     map[string]*sync.Cond
	dialer      proxy.Dialer
}

func newUtlsRoundTripper(proxyURL string) *utlsRoundTripper {
	var dialer proxy.Dialer = proxy.Direct
	if proxyURL != "" {
		proxyDialer, mode, errBuild := proxyutil.BuildDialer(proxyURL)
		if errBuild != nil {
			log.Errorf("utls: failed to configure proxy dialer for %q: %v", proxyutil.Redact(proxyURL), errBuild)
		} else if mode != proxyutil.ModeInherit && proxyDialer != nil {
			dialer = proxyDialer
		}
	}
	return &utlsRoundTripper{
		connections: make(map[string]*utlsClientConnection),
		pending:     make(map[string]*sync.Cond),
		dialer:      dialer,
	}
}

func (t *utlsRoundTripper) getOrCreateConnection(host, addr string) (*utlsClientConnection, error) {
	for {
		t.mu.Lock()

		if cached, ok := t.connections[host]; ok {
			if cached.conn.CanTakeNewRequest() && cached.acquire() {
				t.mu.Unlock()
				return cached, nil
			}
			delete(t.connections, host)
			t.mu.Unlock()
			cached.retire()
			continue
		}

		if cond, ok := t.pending[host]; ok {
			cond.Wait()
			t.mu.Unlock()
			continue
		}

		cond := sync.NewCond(&t.mu)
		t.pending[host] = cond
		t.mu.Unlock()

		h2Conn, err := t.createConnection(host, addr)

		t.mu.Lock()
		delete(t.pending, host)
		cond.Broadcast()
		if err != nil {
			t.mu.Unlock()
			return nil, err
		}

		tracked := &utlsClientConnection{conn: h2Conn}
		tracked.acquire()
		t.connections[host] = tracked
		t.mu.Unlock()
		return tracked, nil
	}
}

func (t *utlsRoundTripper) createConnection(host, addr string) (*http2.ClientConn, error) {
	conn, err := t.dialer.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{ServerName: host}
	tlsConn := tls.UClient(conn, tlsConfig, tls.HelloChrome_Auto)

	if err := tlsConn.Handshake(); err != nil {
		conn.Close()
		return nil, err
	}

	tr := &http2.Transport{}
	h2Conn, err := tr.NewClientConn(tlsConn)
	if err != nil {
		tlsConn.Close()
		return nil, err
	}

	return h2Conn, nil
}

func (t *utlsRoundTripper) retireConnection(host string, connection *utlsClientConnection) {
	t.mu.Lock()
	if cached, ok := t.connections[host]; ok && cached == connection {
		delete(t.connections, host)
	}
	t.mu.Unlock()
	connection.retire()
}

func (t *utlsRoundTripper) closeIdleConnections(now time.Time) {
	var idle []*utlsClientConnection

	t.mu.Lock()
	for host, connection := range t.connections {
		if connection.isIdle(now) {
			delete(t.connections, host)
			idle = append(idle, connection)
		}
	}
	t.mu.Unlock()

	for _, connection := range idle {
		connection.retire()
	}
}

// trackedResponseBody keeps a connection referenced until the caller closes
// or fully consumes the upstream response body, including streaming responses.
type trackedResponseBody struct {
	io.ReadCloser
	release func()
	once    sync.Once
}

func (b *trackedResponseBody) releaseConnection() {
	b.once.Do(b.release)
}

func (b *trackedResponseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.releaseConnection()
	}
	return n, err
}

func (b *trackedResponseBody) Close() error {
	err := b.ReadCloser.Close()
	b.releaseConnection()
	return err
}

func (t *utlsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	hostname := req.URL.Hostname()
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}
	addr := net.JoinHostPort(hostname, port)

	connection, err := t.getOrCreateConnection(hostname, addr)
	if err != nil {
		return nil, err
	}

	resp, err := connection.conn.RoundTrip(req)
	if err != nil {
		t.retireConnection(hostname, connection)
		connection.release()
		return nil, err
	}
	if resp.Body == nil {
		connection.release()
		return resp, nil
	}

	resp.Body = &trackedResponseBody{
		ReadCloser: resp.Body,
		release:    connection.release,
	}
	return resp, nil
}

// utlsTransportPool reuses one uTLS round tripper per proxy/auth scope. The
// pool is process-local because the transport owns live sockets and cannot be
// safely serialized or shared across CPA processes.
type utlsTransportPool struct {
	mu         sync.Mutex
	transports map[string]*utlsRoundTripper
	startOnce  sync.Once
}

func newUtlsTransportPool() *utlsTransportPool {
	return &utlsTransportPool{transports: make(map[string]*utlsRoundTripper)}
}

var defaultUtlsTransportPool = newUtlsTransportPool()

func (p *utlsTransportPool) get(key, proxyURL string) *utlsRoundTripper {
	p.startOnce.Do(func() {
		go p.reapLoop()
	})

	p.mu.Lock()
	defer p.mu.Unlock()
	if transport, ok := p.transports[key]; ok {
		return transport
	}
	transport := newUtlsRoundTripper(proxyURL)
	p.transports[key] = transport
	return transport
}

func (p *utlsTransportPool) reapLoop() {
	ticker := time.NewTicker(utlsReaperInterval)
	defer ticker.Stop()
	for now := range ticker.C {
		p.mu.Lock()
		transports := make([]*utlsRoundTripper, 0, len(p.transports))
		for _, transport := range p.transports {
			transports = append(transports, transport)
		}
		p.mu.Unlock()

		for _, transport := range transports {
			transport.closeIdleConnections(now)
		}
	}
}

func utlsTransportPoolKey(proxyURL string, auth *cliproxyauth.Auth) string {
	if auth == nil {
		return proxyURL
	}
	scope := auth.ID
	if scope == "" {
		scope = auth.Index
	}
	if scope == "" {
		return proxyURL
	}
	// Keep credentials from different auth records isolated while still
	// reusing connections for repeated requests from the same record.
	return proxyURL + "\x00" + scope
}

// utlsProtectedHosts contains the hosts that should use utls Chrome TLS fingerprint
// to bypass Cloudflare's TLS fingerprinting.
var utlsProtectedHosts = map[string]struct{}{
	"api.anthropic.com": {},
	"chatgpt.com":       {},
}

// fallbackRoundTripper uses utls for protected HTTPS hosts and falls back to
// standard transport for all other requests.
type fallbackRoundTripper struct {
	utls     http.RoundTripper
	fallback http.RoundTripper
}

func (f *fallbackRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme == "https" {
		if _, ok := utlsProtectedHosts[strings.ToLower(req.URL.Hostname())]; ok {
			return f.utls.RoundTrip(req)
		}
	}
	return f.fallback.RoundTrip(req)
}

// NewUtlsHTTPClient creates an HTTP client using utls Chrome TLS fingerprint.
// Use this for provider requests that need a Chrome-like TLS fingerprint.
// Falls back to standard transport for non-HTTPS requests.
func NewUtlsHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	var proxyURL string
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}

	var ctxRoundTripper http.RoundTripper
	if ctx != nil {
		ctxRoundTripper, _ = ctx.Value("cliproxy.roundtripper").(http.RoundTripper)
	}

	var utlsRT http.RoundTripper
	var standardTransport http.RoundTripper = http.DefaultTransport
	if proxyURL == "" && ctxRoundTripper != nil {
		utlsRT = ctxRoundTripper
		standardTransport = ctxRoundTripper
	} else {
		utlsRT = defaultUtlsTransportPool.get(utlsTransportPoolKey(proxyURL, auth), proxyURL)
		if proxyURL != "" {
			if transport := buildProxyTransport(proxyURL); transport != nil {
				standardTransport = transport
			}
		}
	}

	client := &http.Client{
		Transport: &fallbackRoundTripper{
			utls:     utlsRT,
			fallback: standardTransport,
		},
	}
	if timeout > 0 {
		client.Timeout = timeout
	}
	return client
}
