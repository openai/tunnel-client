package mockproxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
)

type RequestRecord struct {
	Method string
	Host   string
	URL    string
}

type ProxyServer struct {
	mu        sync.Mutex
	server    *httptest.Server
	routes    map[string]*url.URL
	records   []RequestRecord
	transport *http.Transport
}

type Option func(*ProxyServer)

func WithRoute(host string, target *url.URL) Option {
	return func(p *ProxyServer) {
		if host == "" || target == nil {
			return
		}
		if p.routes == nil {
			p.routes = make(map[string]*url.URL)
		}
		p.routes[host] = target
	}
}

func New(opts ...Option) *ProxyServer {
	proxy := &ProxyServer{
		routes: make(map[string]*url.URL),
	}
	for _, opt := range opts {
		opt(proxy)
	}
	proxy.transport = http.DefaultTransport.(*http.Transport).Clone()
	proxy.transport.Proxy = nil
	return proxy
}

func (p *ProxyServer) Start() {
	if p.server != nil {
		return
	}
	p.server = httptest.NewServer(http.HandlerFunc(p.handle))
}

func (p *ProxyServer) Close() {
	if p.server != nil {
		p.server.Close()
		p.server = nil
	}
}

func (p *ProxyServer) URL() string {
	if p.server == nil {
		return ""
	}
	return p.server.URL
}

func (p *ProxyServer) Records() []RequestRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]RequestRecord, len(p.records))
	copy(out, p.records)
	return out
}

func (p *ProxyServer) SetRoute(host string, target *url.URL) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if host == "" || target == nil {
		return
	}
	p.routes[host] = target
}

func (p *ProxyServer) handle(w http.ResponseWriter, r *http.Request) {
	p.record(r)
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}
	p.handleForward(w, r)
}

func (p *ProxyServer) handleForward(w http.ResponseWriter, r *http.Request) {
	target := p.routeForHost(r.URL.Host)
	if target == nil {
		http.Error(w, "proxy route not found", http.StatusBadGateway)
		return
	}
	targetURL := *target
	targetURL.Path = r.URL.Path
	targetURL.RawQuery = r.URL.RawQuery
	req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL.String(), r.Body)
	if err != nil {
		http.Error(w, "proxy request build failed", http.StatusBadGateway)
		return
	}
	req.Header = r.Header.Clone()
	resp, err := p.transport.RoundTrip(req)
	if err != nil {
		http.Error(w, "proxy forward failed", http.StatusBadGateway)
		return
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (p *ProxyServer) handleConnect(w http.ResponseWriter, r *http.Request) {
	target := p.routeForHost(r.Host)
	if target == nil {
		http.Error(w, "proxy route not found", http.StatusBadGateway)
		return
	}
	destConn, err := net.Dial("tcp", target.Host)
	if err != nil {
		http.Error(w, "proxy dial failed", http.StatusBadGateway)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = destConn.Close()
		http.Error(w, "proxy hijack unsupported", http.StatusBadGateway)
		return
	}
	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		_ = destConn.Close()
		return
	}
	_, _ = fmt.Fprint(clientConn, "HTTP/1.1 200 Connection Established\r\n\r\n")
	go proxyCopy(clientConn, destConn)
	go proxyCopy(destConn, clientConn)
}

func (p *ProxyServer) record(r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.records = append(p.records, RequestRecord{
		Method: r.Method,
		Host:   r.Host,
		URL:    r.URL.String(),
	})
}

func (p *ProxyServer) routeForHost(host string) *url.URL {
	p.mu.Lock()
	defer p.mu.Unlock()
	if target, ok := p.routes[host]; ok {
		return target
	}
	if trimmed, _, err := net.SplitHostPort(host); err == nil {
		if target, ok := p.routes[trimmed]; ok {
			return target
		}
	}
	return nil
}

func proxyCopy(dst net.Conn, src net.Conn) {
	_, _ = io.Copy(dst, src)
	_ = dst.Close()
	_ = src.Close()
}

func WithRoutes(routes map[string]*url.URL) Option {
	return func(p *ProxyServer) {
		for host, target := range routes {
			WithRoute(host, target)(p)
		}
	}
}

func WithContext(ctx context.Context) Option {
	return func(p *ProxyServer) {
		go func() {
			<-ctx.Done()
			p.Close()
		}()
	}
}
