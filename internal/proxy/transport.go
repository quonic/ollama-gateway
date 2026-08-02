package proxy

import (
	"context"
	"net"
	"net/http"
	"time"

	"ollama-gateway/internal/backends"
)

// defaultTransportTimeout is the per-request timeout applied when a backend does not specify one.
const defaultTransportTimeout = 60 * time.Second

// newTransport creates an http.Transport configured for proxying to Ollama backends.
// It enables keep-alive connections, sets reasonable timeouts, and disables HTTP/2 so that
// streaming responses (newline-delimited JSON) are flushed promptly without chunked encoding issues.
func newTransport(backend *backends.Backend) *http.Transport {
	timeout := backend.Timeout
	if timeout <= 0 {
		timeout = defaultTransportTimeout
	}

	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           newDialContext(timeout),
		MaxIdleConns:          128,
		MaxIdleConnsPerHost:   32,
		MaxConnsPerHost:       64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   timeout,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     false, // keep streaming simple; Ollama uses HTTP/1.1 chunked SSE-like format
	}
}

// newDialContext returns a dial function with the given timeout for TCP connections to backends.
func newDialContext(timeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: timeout}
	return dialer.DialContext
}

// applyHeaders adds any backend-specific extra headers to the outgoing request before it is sent.
func applyHeaders(req *http.Request, headers map[string]string) {
	for k, v := range headers {
		req.Header.Set(k, v)
	}
}
