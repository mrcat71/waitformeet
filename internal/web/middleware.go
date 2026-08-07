package web

import (
	"net"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strings"
	"time"
)

const (
	// readinessTimeout bounds the database ping behind /readyz.
	readinessTimeout = 2 * time.Second
)

// securityHeaders sets the headers that apply to every response.
//
// The content security policy is strict on purpose: this site loads no third-party
// scripts, fonts or images, so anything from another origin is a bug or an attack.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	csp := strings.Join([]string{
		"default-src 'self'",
		"script-src 'self'",
		// Inline styles are needed for the per-deployment accent colour, which is a
		// value from the database rather than something an attacker controls.
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data:",
		"font-src 'self'",
		"connect-src 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'",
		"base-uri 'self'",
		"object-src 'none'",
	}, "; ")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		// Nothing here needs a camera, a microphone or a location.
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), interest-cohort=()")

		if s.cfg.CookieSecure {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}

		next.ServeHTTP(w, r)
	})
}

// recoverPanic turns a panic in a handler into a 500 instead of a dropped
// connection, and logs the stack so the cause is not lost.
func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.ErrorContext(r.Context(), "handler panicked",
					"error", rec,
					"method", r.Method,
					"path", r.URL.Path,
					"stack", string(debug.Stack()),
				)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// statusRecorder remembers the status code so the access log can report it.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Flush forwards to the underlying writer when it supports flushing, so streaming
// responses such as the export download are not buffered by this wrapper.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Health probes would otherwise dominate the log.
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)

		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		s.log.InfoContext(r.Context(), "request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"bytes", rec.bytes,
			"duration", time.Since(start).Round(time.Millisecond).String(),
			"ip", s.clientIP(r),
		)
	})
}

// clientIP resolves the caller's address.
//
// X-Forwarded-For is only believed when the immediate peer is a configured trusted
// proxy. Otherwise any visitor could forge their own address and defeat the login
// rate limiter by rotating it.
func (s *Server) clientIP(r *http.Request) string {
	peer := remoteAddr(r)

	if len(s.cfg.TrustedProxies) == 0 || !s.isTrustedProxy(peer) {
		return peer.String()
	}

	forwarded := r.Header.Get("X-Forwarded-For")
	if forwarded == "" {
		return peer.String()
	}

	// Walk right to left, skipping hops we trust, and take the first address that
	// we do not: that is the closest one the chain did not vouch for.
	parts := strings.Split(forwarded, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
		if err != nil {
			continue
		}
		if !s.isTrustedProxy(addr) {
			return addr.String()
		}
	}
	return peer.String()
}

func (s *Server) isTrustedProxy(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	for _, prefix := range s.cfg.TrustedProxies {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func remoteAddr(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	// A v4-mapped v6 address must be unmapped before comparing against v4 prefixes.
	return addr.Unmap()
}
