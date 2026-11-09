package httpguard

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
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
	return SameOriginUnsafeWithPublicOrigin(next, message, nil)
}

// SameOriginUnsafeWithPublicOrigin additionally permits an explicitly configured
// public browser origin. It does not change network access or trust proxy headers.
// The caller must validate publicOrigin as an HTTP(S) origin and apply the
// appropriate network access guard before this handler.
func SameOriginUnsafeWithPublicOrigin(next http.Handler, message string, publicOrigin *url.URL) http.Handler {
	if message == "" {
		message = defaultSameOriginMessage
	}
	var allowedOrigin *url.URL
	if publicOrigin != nil {
		origin := *publicOrigin
		allowedOrigin = &origin
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isUnsafeMethod(r.Method) && !isSameOriginOrPublicBrowserRequest(r, allowedOrigin) {
			http.Error(w, message, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isSameOriginOrPublicBrowserRequest(r *http.Request, publicOrigin *url.URL) bool {
	if IsSameOriginBrowserRequest(r) {
		return true
	}
	if r == nil || publicOrigin == nil || strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
		return false
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		return matchesPublicOrigin(origin, publicOrigin, false)
	}
	if referer := strings.TrimSpace(r.Header.Get("Referer")); referer != "" {
		return matchesPublicOrigin(referer, publicOrigin, true)
	}
	return false
}

func matchesPublicOrigin(raw string, publicOrigin *url.URL, allowPath bool) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Opaque != "" || parsed.Hostname() == "" {
		return false
	}
	if !allowPath && ((parsed.Path != "" && parsed.Path != "/") || parsed.RawPath != "" ||
		parsed.RawQuery != "" || parsed.ForceQuery || strings.Contains(raw, "#")) {
		return false
	}
	if publicOrigin == nil || !strings.EqualFold(parsed.Scheme, publicOrigin.Scheme) {
		return false
	}
	host := originHostname(parsed)
	port := originPort(parsed)
	return host != "" && port != "" && host == originHostname(publicOrigin) && port == originPort(publicOrigin)
}

func originHostname(origin *url.URL) string {
	if origin == nil {
		return ""
	}
	host := strings.ToLower(origin.Hostname())
	if strings.ContainsAny(host, "*%") {
		return ""
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return addr.String()
	}
	return host
}

func originPort(origin *url.URL) string {
	if origin == nil || strings.HasSuffix(origin.Host, ":") {
		return ""
	}
	defaultPort := ""
	switch strings.ToLower(origin.Scheme) {
	case "http":
		defaultPort = "80"
	case "https":
		defaultPort = "443"
	default:
		return ""
	}
	if port := origin.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return ""
		}
		return strconv.Itoa(n)
	}
	return defaultPort
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
	if !strings.EqualFold(parsed.Scheme, requestScheme(r)) || !strings.EqualFold(parsed.Host, r.Host) {
		return false
	}
	if IsLoopbackRequest(r) && !isLocalAdminHost(parsed.Hostname()) {
		return false
	}
	return true
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

func isLocalAdminHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
