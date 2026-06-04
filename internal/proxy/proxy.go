// Package proxy provides a shared HTTP client with optional SOCKS5 proxy support.
package proxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/net/proxy"
)

var (
	defaultClient = &http.Client{Timeout: 30 * time.Second}
	rawURL        string
	socks5Dialer  proxy.Dialer
)

// Init configures the package-level state from the given proxy URL.
// rawURL must be a socks5:// address (e.g. "socks5://user:pass@host:1080").
// Call once at startup before any network requests are made.
func Init(enabled bool, proxyURL string) error {
	if !enabled || proxyURL == "" {
		defaultClient = &http.Client{Timeout: 30 * time.Second}
		rawURL = ""
		socks5Dialer = nil
		return nil
	}

	u, err := url.Parse(proxyURL)
	if err != nil {
		return fmt.Errorf("proxy: invalid URL %q: %w", proxyURL, err)
	}

	var auth *proxy.Auth
	if u.User != nil {
		auth = &proxy.Auth{User: u.User.Username()}
		if p, ok := u.User.Password(); ok {
			auth.Password = p
		}
	}

	dialer, err := proxy.SOCKS5("tcp", u.Host, auth, proxy.Direct)
	if err != nil {
		return fmt.Errorf("proxy: SOCKS5 dialer: %w", err)
	}

	dialFn := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if cd, ok := dialer.(proxy.ContextDialer); ok {
			return cd.DialContext(ctx, network, addr)
		}
		return dialer.Dial(network, addr)
	}

	defaultClient = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: dialFn,
		},
	}
	rawURL = proxyURL
	socks5Dialer = dialer
	return nil
}

// HTTPClient returns the shared HTTP client (proxy-aware if Init was called with a valid proxy).
func HTTPClient() *http.Client {
	return defaultClient
}

// URL returns the configured proxy URL, or "" if proxy is disabled.
func URL() string {
	return rawURL
}

// Dialer returns the SOCKS5 dialer, or nil if proxy is disabled.
// Use for raw TCP connections that need to be proxied (e.g. MQTT over TCP).
func Dialer() proxy.Dialer {
	return socks5Dialer
}
