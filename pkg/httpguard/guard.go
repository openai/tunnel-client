package httpguard

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
)

const defaultLoopbackMessage = "access is restricted to loopback; set --allow-remote-ui to override"
const defaultSameOriginMessage = "unsafe browser request must be same-origin"

type connectionNetworkKey struct{}

// GuardedMux wraps an http.ServeMux with loopback gating.
type GuardedMux struct {
	mux         *http.ServeMux
	allowRemote bool
	message     string
}

// NewGuardedMux constructs a GuardedMux using the supplied loopback policy.
func NewGuardedMux(mux *http.ServeMux, allowRemote bool, message string) GuardedMux {
	if message == "" {
		message = defaultLoopbackMessage
	}
	return GuardedMux{
		mux:         mux,
		allowRemote: allowRemote,
		message:     message,
	}
}

// Handle registers a pattern with loopback enforcement applied.
func (g GuardedMux) Handle(pattern string, h http.Handler) {
	if g.mux == nil {
		return
	}
	g.mux.Handle(pattern, g.guard(h))
}

// HandleFunc registers a pattern with loopback enforcement applied.
func (g GuardedMux) HandleFunc(pattern string, fn func(http.ResponseWriter, *http.Request)) {
	g.Handle(pattern, http.HandlerFunc(fn))
}

func (g GuardedMux) guard(next http.Handler) http.Handler {
	if g.allowRemote {
		return next
	}
	return LocalOnly(next, g.message)
}

// LocalOnly blocks non-loopback traffic with a configurable error message.
func LocalOnly(next http.Handler, message string) http.Handler {
	if message == "" {
		message = defaultLoopbackMessage
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsLoopbackRequest(r) {
			http.Error(w, message, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// SameOriginUnsafe rejects unsafe browser requests that do not originate from
// the admin UI origin. Requests without browser CSRF headers are allowed so
// local non-browser clients keep working.
func SameOriginUnsafe(next http.Handler, message string) http.Handler {
	if message == "" {
		message = defaultSameOriginMessage
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isUnsafeMethod(r.Method) && !IsSameOriginBrowserRequest(r) {
			http.Error(w, message, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isUnsafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return false
	default:
		return true
	}
}

// IsSameOriginBrowserRequest reports whether a browser-originated unsafe
// request came from the same origin as the admin endpoint.
func IsSameOriginBrowserRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
		return false
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		return requestURLMatchesOrigin(r, origin)
	}
	if referer := strings.TrimSpace(r.Header.Get("Referer")); referer != "" {
		return requestURLMatchesOrigin(r, referer)
	}
	return true
}

func requestURLMatchesOrigin(r *http.Request, raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Scheme, requestScheme(r)) && strings.EqualFold(parsed.Host, r.Host)
}

func requestScheme(r *http.Request) string {
	if r != nil && r.TLS != nil {
		return "https"
	}
	if r != nil && r.URL != nil && r.URL.Scheme != "" {
		return r.URL.Scheme
	}
	return "http"
}

// IsLoopbackRequest reports whether a request originates from loopback.
func IsLoopbackRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if ConnectionNetwork(r.Context()) == "unix" {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

func WithConnectionNetwork(ctx context.Context, network string) context.Context {
	return context.WithValue(ctx, connectionNetworkKey{}, network)
}

func ConnectionNetwork(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	network, _ := ctx.Value(connectionNetworkKey{}).(string)
	return network
}

// WithShutdownContext merges the request context with a shutdown context.
// The handler will observe cancellation from either context.
func WithShutdownContext(next http.Handler, shutdownCtx context.Context) http.Handler {
	if shutdownCtx == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r == nil {
			next.ServeHTTP(w, r)
			return
		}
		ctx := MergeContexts(r.Context(), shutdownCtx)
		if ctx == r.Context() {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// MergeContexts returns a context that is canceled when either input context is done.
func MergeContexts(primary context.Context, secondary context.Context) context.Context {
	if primary == nil {
		primary = context.Background()
	}
	if secondary == nil {
		return primary
	}
	ctx, cancel := context.WithCancel(primary)
	go func() {
		select {
		case <-secondary.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx
}
