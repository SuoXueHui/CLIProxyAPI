package proxyutil

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

// Mode describes how a proxy setting should be interpreted.
type Mode int

const (
	// ModeInherit means no explicit proxy behavior was configured.
	ModeInherit Mode = iota
	// ModeDirect means outbound requests must bypass proxies explicitly.
	ModeDirect
	// ModeProxy means a concrete proxy URL was configured.
	ModeProxy
	// ModeInvalid means the proxy setting is present but malformed or unsupported.
	ModeInvalid
)

// Setting is the normalized interpretation of a proxy configuration value.
type Setting struct {
	Raw  string
	Mode Mode
	URL  *url.URL
}

// Options controls optional source binding for proxy transports.
// SourceIP must be a local IPv6 address; an empty value preserves the
// historical behavior of the compatibility builders below.
type Options struct {
	SourceIP string
}

// Parse normalizes a proxy configuration value into inherit, direct, or proxy modes.
func Parse(raw string) (Setting, error) {
	trimmed := strings.TrimSpace(raw)
	setting := Setting{Raw: trimmed}

	if trimmed == "" {
		setting.Mode = ModeInherit
		return setting, nil
	}

	if strings.EqualFold(trimmed, "direct") || strings.EqualFold(trimmed, "none") {
		setting.Mode = ModeDirect
		return setting, nil
	}

	parsedURL, errParse := url.Parse(trimmed)
	if errParse != nil {
		setting.Mode = ModeInvalid
		return setting, fmt.Errorf("parse proxy URL failed")
	}
	if parsedURL.Scheme == "" || parsedURL.Host == "" {
		setting.Mode = ModeInvalid
		return setting, fmt.Errorf("proxy URL missing scheme/host")
	}

	switch parsedURL.Scheme {
	case "socks5", "socks5h", "http", "https":
		setting.Mode = ModeProxy
		setting.URL = parsedURL
		return setting, nil
	default:
		setting.Mode = ModeInvalid
		return setting, fmt.Errorf("unsupported proxy scheme: %s", parsedURL.Scheme)
	}
}

func cloneDefaultTransport() *http.Transport {
	if transport, ok := http.DefaultTransport.(*http.Transport); ok && transport != nil {
		return transport.Clone()
	}
	return &http.Transport{}
}

// NewDirectTransport returns a transport that bypasses environment proxies.
func NewDirectTransport() *http.Transport {
	clone := cloneDefaultTransport()
	clone.Proxy = nil
	return clone
}

// BuildHTTPTransport constructs an HTTP transport for the provided proxy setting.
func BuildHTTPTransport(raw string) (*http.Transport, Mode, error) {
	return BuildHTTPTransportWithOptions(raw, Options{})
}

// BuildHTTPTransportWithOptions constructs an HTTP transport and optionally
// binds every outbound TCP connection to a local IPv6 source address.
func BuildHTTPTransportWithOptions(raw string, options Options) (*http.Transport, Mode, error) {
	localAddr, errLocalAddr := parseSourceIPv6(options.SourceIP)
	if errLocalAddr != nil {
		return nil, ModeInvalid, errLocalAddr
	}

	setting, errParse := Parse(raw)
	if errParse != nil {
		return nil, setting.Mode, errParse
	}

	switch setting.Mode {
	case ModeInherit:
		if localAddr != nil {
			transport := cloneDefaultTransport()
			applyLocalAddr(transport, localAddr)
			return transport, setting.Mode, nil
		}
		return nil, setting.Mode, nil
	case ModeDirect:
		transport := NewDirectTransport()
		applyLocalAddr(transport, localAddr)
		return transport, setting.Mode, nil
	case ModeProxy:
		if setting.URL.Scheme == "socks5" || setting.URL.Scheme == "socks5h" {
			var proxyAuth *proxy.Auth
			if setting.URL.User != nil {
				username := setting.URL.User.Username()
				password, _ := setting.URL.User.Password()
				proxyAuth = &proxy.Auth{User: username, Password: password}
			}
			forwardDialer := proxy.Dialer(proxy.Direct)
			if localAddr != nil {
				forwardDialer = &net.Dialer{LocalAddr: localAddr}
			}
			dialer, errSOCKS5 := proxy.SOCKS5("tcp", setting.URL.Host, proxyAuth, forwardDialer)
			if errSOCKS5 != nil {
				return nil, setting.Mode, fmt.Errorf("create SOCKS5 dialer failed: %w", errSOCKS5)
			}
			transport := cloneDefaultTransport()
			transport.Proxy = nil
			transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				if contextDialer, ok := dialer.(proxy.ContextDialer); ok {
					return contextDialer.DialContext(ctx, network, addr)
				}
				return dialer.Dial(network, addr)
			}
			return transport, setting.Mode, nil
		}
		if setting.URL.Scheme == "https" {
			transport := cloneDefaultTransport()
			transport.Proxy = http.ProxyURL(setting.URL)
			transport.DialTLSContext = buildHTTPSProxyDialTLSContext(setting.URL, nil, transport.TLSHandshakeTimeout, transport.DialContext)
			return transport, setting.Mode, nil
		}
		transport := cloneDefaultTransport()
		transport.Proxy = http.ProxyURL(setting.URL)
		applyLocalAddr(transport, localAddr)
		return transport, setting.Mode, nil
	default:
		return nil, setting.Mode, nil
	}
}

func buildHTTPSProxyDialTLSContext(
	proxyURL *url.URL,
	baseTLS *tls.Config,
	handshakeTimeout time.Duration,
	baseDialContext func(ctx context.Context, network, addr string) (net.Conn, error),
) func(ctx context.Context, network, addr string) (net.Conn, error) {
	proxyHostname := proxyURL.Hostname()
	var privateTLS *tls.Config
	if baseTLS != nil {
		privateTLS = baseTLS.Clone()
	}
	dialContext := baseDialContext
	if dialContext == nil {
		dialContext = (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		rawConn, errDial := dialContext(ctx, network, addr)
		if errDial != nil {
			return nil, errDial
		}
		var tlsConfig *tls.Config
		if privateTLS != nil {
			tlsConfig = privateTLS.Clone()
		} else {
			tlsConfig = &tls.Config{}
		}
		if tlsConfig.ServerName == "" {
			tlsConfig.ServerName = proxyHostname
		}
		tlsConfig.NextProtos = []string{"http/1.1"}

		tlsConn := tls.Client(rawConn, tlsConfig)
		handshakeCtx := ctx
		if handshakeTimeout > 0 {
			var cancelHandshake context.CancelFunc
			handshakeCtx, cancelHandshake = context.WithTimeout(ctx, handshakeTimeout)
			defer cancelHandshake()
		}
		if errHandshake := tlsConn.HandshakeContext(handshakeCtx); errHandshake != nil {
			if errClose := rawConn.Close(); errClose != nil {
				return nil, fmt.Errorf("HTTPS proxy TLS handshake failed: %w; close failed: %v", errHandshake, errClose)
			}
			return nil, fmt.Errorf("HTTPS proxy TLS handshake failed: %w", errHandshake)
		}
		return tlsConn, nil
	}
}

// BuildDialer constructs a proxy dialer for settings that operate at the connection layer.
func BuildDialer(raw string) (proxy.Dialer, Mode, error) {
	return BuildDialerWithOptions(raw, Options{})
}

// BuildDialerWithOptions constructs a connection-level proxy dialer and
// optionally binds its connection to a local IPv6 source address.
func BuildDialerWithOptions(raw string, options Options) (proxy.Dialer, Mode, error) {
	localAddr, errLocalAddr := parseSourceIPv6(options.SourceIP)
	if errLocalAddr != nil {
		return nil, ModeInvalid, errLocalAddr
	}

	setting, errParse := Parse(raw)
	if errParse != nil {
		return nil, setting.Mode, errParse
	}

	switch setting.Mode {
	case ModeInherit:
		if localAddr != nil {
			return &net.Dialer{LocalAddr: localAddr}, setting.Mode, nil
		}
		return nil, setting.Mode, nil
	case ModeDirect:
		if localAddr != nil {
			return &net.Dialer{LocalAddr: localAddr}, setting.Mode, nil
		}
		return proxy.Direct, setting.Mode, nil
	case ModeProxy:
		if setting.URL.Scheme == "http" || setting.URL.Scheme == "https" {
			proxyDialer := proxy.Dialer(proxy.Direct)
			if localAddr != nil {
				proxyDialer = &net.Dialer{LocalAddr: localAddr}
			}
			return &httpConnectDialer{proxyURL: setting.URL, dialer: proxyDialer}, setting.Mode, nil
		}
		forwardDialer := proxy.Dialer(proxy.Direct)
		if localAddr != nil {
			forwardDialer = &net.Dialer{LocalAddr: localAddr}
		}
		dialer, errDialer := proxy.FromURL(setting.URL, forwardDialer)
		if errDialer != nil {
			return nil, setting.Mode, fmt.Errorf("create proxy dialer failed: %w", errDialer)
		}
		return dialer, setting.Mode, nil
	default:
		return nil, setting.Mode, nil
	}
}

func parseSourceIPv6(raw string) (*net.TCPAddr, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	if strings.Contains(trimmed, "%") {
		return nil, fmt.Errorf("source IP must be a global IPv6 address")
	}
	ip := net.ParseIP(trimmed)
	if ip == nil {
		return nil, fmt.Errorf("invalid source IPv6 address")
	}
	if ip.To4() != nil {
		return nil, fmt.Errorf("source IP must be an IPv6 address")
	}
	return &net.TCPAddr{IP: ip.To16()}, nil
}

func applyLocalAddr(transport *http.Transport, localAddr *net.TCPAddr) {
	if localAddr == nil {
		return
	}
	dialer := &net.Dialer{LocalAddr: localAddr}
	transport.DialContext = dialer.DialContext
}

type httpConnectDialer struct {
	proxyURL  *url.URL
	dialer    proxy.Dialer
	tlsConfig *tls.Config
}

func (d *httpConnectDialer) Dial(network, addr string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, addr)
}

func (d *httpConnectDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	contextDialer, ok := d.dialer.(proxy.ContextDialer)
	if !ok {
		return nil, errors.New("HTTP proxy base dialer does not support context cancellation")
	}
	proxyConn, errDial := contextDialer.DialContext(ctx, network, proxyDialAddr(d.proxyURL))
	if errDial != nil {
		return nil, fmt.Errorf("dial HTTP proxy failed: %w", errDial)
	}

	conn := proxyConn
	cancelDone := make(chan struct{})
	stopCancel := context.AfterFunc(ctx, func() {
		_ = proxyConn.Close()
		close(cancelDone)
	})
	defer func() {
		if !stopCancel() {
			<-cancelDone
		}
	}()
	if d.proxyURL.Scheme == "https" {
		var tlsConfig *tls.Config
		if d.tlsConfig != nil {
			tlsConfig = d.tlsConfig.Clone()
		} else {
			tlsConfig = &tls.Config{}
		}
		if tlsConfig.ServerName == "" {
			tlsConfig.ServerName = d.proxyURL.Hostname()
		}
		tlsConfig.NextProtos = []string{"http/1.1"}

		tlsConn := tls.Client(conn, tlsConfig)
		if errHandshake := tlsConn.HandshakeContext(ctx); errHandshake != nil {
			if errClose := conn.Close(); errClose != nil {
				return nil, fmt.Errorf("HTTPS proxy TLS handshake failed: %w; close failed: %v", errHandshake, errClose)
			}
			return nil, fmt.Errorf("HTTPS proxy TLS handshake failed: %w", errHandshake)
		}
		conn = tlsConn
	}

	req := (&http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Host: addr},
		Host:   addr,
		Header: make(http.Header),
	}).WithContext(ctx)
	if d.proxyURL.User != nil {
		req.Header.Set("Proxy-Authorization", proxyAuthorization(d.proxyURL.User))
	}
	if errWrite := req.Write(conn); errWrite != nil {
		if errClose := conn.Close(); errClose != nil {
			return nil, fmt.Errorf("write CONNECT request failed: %w; close failed: %v", errWrite, errClose)
		}
		return nil, fmt.Errorf("write CONNECT request failed: %w", errWrite)
	}

	reader := bufio.NewReader(conn)
	resp, errRead := http.ReadResponse(reader, req)
	if errRead != nil {
		if errClose := conn.Close(); errClose != nil {
			return nil, fmt.Errorf("read CONNECT response failed: %w; close failed: %v", errRead, errClose)
		}
		return nil, fmt.Errorf("read CONNECT response failed: %w", errRead)
	}
	if resp.StatusCode != http.StatusOK {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
		if errClose := conn.Close(); errClose != nil {
			return nil, fmt.Errorf("proxy CONNECT returned status %s; close failed: %v", resp.Status, errClose)
		}
		return nil, fmt.Errorf("proxy CONNECT returned status %s", resp.Status)
	}

	if errContext := ctx.Err(); errContext != nil {
		if errClose := conn.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			return nil, fmt.Errorf("HTTP proxy context ended: %w; close failed: %v", errContext, errClose)
		}
		return nil, errContext
	}
	if reader.Buffered() > 0 {
		return &bufferedConn{Conn: conn, reader: reader}, nil
	}
	return conn, nil
}

func proxyDialAddr(proxyURL *url.URL) string {
	port := proxyURL.Port()
	if port == "" {
		port = "80"
		if proxyURL.Scheme == "https" {
			port = "443"
		}
	}
	return net.JoinHostPort(proxyURL.Hostname(), port)
}

func proxyAuthorization(user *url.Userinfo) string {
	username := user.Username()
	password, _ := user.Password()
	encoded := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	return "Basic " + encoded
}

// Redact returns a log-safe proxy URL with credentials and path-like data removed.
func Redact(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}

	parsedURL, errParse := url.Parse(trimmed)
	if errParse != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return "<invalid proxy URL>"
	}

	redacted := &url.URL{
		Scheme: parsedURL.Scheme,
		Host:   parsedURL.Host,
	}
	if parsedURL.User != nil {
		redacted.User = url.User("redacted")
	}
	return redacted.String()
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	if c.reader.Buffered() > 0 {
		return c.reader.Read(p)
	}
	return c.Conn.Read(p)
}
