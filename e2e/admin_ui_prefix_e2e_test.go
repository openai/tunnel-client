package e2e_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
)

const adminUIPrefixPublicOrigin = "https://admin.example.test"

var adminUIPrefixJSONRoutes = []string{
	"/api/status",
	"/api/system",
	"/api/oauth",
	"/api/logs?limit=1",
	"/api/log-level",
	"/api/harpoon/status",
	"/api/harpoon/targets",
	"/api/harpoon/calls?limit=1",
	"/api/codex/status",
	"/api/codex/events?limit=1",
}

// TestRuntimeAdminUIProxyPrefixes exercises the shipped binary through the two
// common proxy path contracts: stripping an external prefix and preserving a
// prefix configured on the application. It checks the HTML and HTTP boundary;
// frontend tests separately exercise the JavaScript URL helper and its callers.
func TestRuntimeAdminUIProxyPrefixes(t *testing.T) {
	runtimeSkipUnixSignals(t)
	t.Parallel()

	subjects := runtimeSubjectsWithBinaries(t, runtimeFullSubject())
	for _, tc := range []struct {
		name         string
		basePath     string
		publicOrigin string
	}{
		{name: "default"},
		{name: "native-prefix", basePath: "/tools/tunnel", publicOrigin: adminUIPrefixPublicOrigin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runRuntimeScenario(t, subjects, func(t *testing.T) runtimeScenario {
				profile, healthFile, pidFile := writeRuntimeCompatibilityProfile(t)
				var args []string
				if tc.basePath != "" {
					args = append(args, "--admin-ui.base-path="+tc.basePath)
				}
				if tc.publicOrigin != "" {
					args = append(args, "--admin-ui.public-origin="+tc.publicOrigin)
				}
				return runtimeScenario{
					name: tc.name, profilePath: profile, healthURLFile: healthFile, pidFile: pidFile,
					options: runtimeRunOptions{
						args: args,
						env: map[string]string{
							"ADMIN_UI_BASE_PATH":     "",
							"ADMIN_UI_PUBLIC_ORIGIN": "",
						},
						afterReady: func(t *testing.T, run *runtimeSubjectRun) {
							assertAdminUIPrefixDeployment(t, run, tc.basePath, tc.publicOrigin != "")
						},
					},
				}
			})
		})
	}
}

type adminUIPrefixFrontend struct {
	name   string
	origin string
	mount  string
}

func assertAdminUIPrefixDeployment(t *testing.T, run *runtimeSubjectRun, basePath string, publicOriginAllowed bool) {
	t.Helper()
	healthURL, err := os.ReadFile(run.healthURLFile)
	require.NoError(t, err)
	backend := strings.TrimSpace(string(healthURL))
	client := adminUIPrefixHTTPClient(t)

	frontends := []adminUIPrefixFrontend{{name: "direct", origin: backend, mount: basePath}}
	if basePath == "" {
		// Both frontends share the same unconfigured binary. This checks
		// that one external mount cannot determine another mount's URLs.
		for _, prefix := range []string{"/tools/tunnel", "/another/nested/tunnel"} {
			frontends = append(frontends, adminUIPrefixFrontend{
				name: "stripped" + prefix, origin: newAdminUIPrefixProxy(t, backend, prefix, true), mount: prefix,
			})
		}
	} else {
		frontends = append(frontends, adminUIPrefixFrontend{
			name: "preserved", origin: newAdminUIPrefixProxy(t, backend, basePath, false), mount: basePath,
		})
	}
	for _, frontend := range frontends {
		t.Run(frontend.name, func(t *testing.T) {
			assertAdminUIPrefixFrontend(t, client, frontend, publicOriginAllowed)
			if basePath != "" {
				assertAdminUIPrefixRedirect(
					t, client, frontend.origin+frontend.mount+"?view=logs&limit=1",
					http.StatusTemporaryRedirect, "tunnel/?view=logs&limit=1", frontend.mount+"/?view=logs&limit=1",
				)
			}
		})
	}
	if basePath != "" {
		// Native mounting moves admin routes while the harness continues
		// to check the original root health routes after this hook.
		routes := []string{
			"/", "/ui", "/ui/", "/assets", "/assets/", "/assets/app.js", "/favicon.ico",
			"/api/logs/export?minutes=7", "/api/logs/stream", "/api/codex/events/stream",
			basePath + "-other/ui",
		}
		routes = append(routes, adminUIPrefixJSONRoutes...)
		for _, route := range routes {
			response, _ := getAdminUIPrefix(t, client, backend+route)
			require.Equalf(t, http.StatusNotFound, response.StatusCode, "unmounted route %s", route)
		}
	}
}

func adminUIPrefixHTTPClient(t *testing.T) *http.Client {
	t.Helper()
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{
		Transport: transport,
		Timeout:   5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func newAdminUIPrefixProxy(t *testing.T, backend, prefix string, strip bool) string {
	t.Helper()
	target, err := url.Parse(backend)
	require.NoError(t, err)
	proxy := httputil.NewSingleHostReverseProxy(target)
	transport := &http.Transport{}
	proxy.Transport = transport
	t.Cleanup(transport.CloseIdleConnections)

	var handler http.Handler = proxy
	if strip {
		handler = http.StripPrefix(prefix, handler)
	}
	mux := http.NewServeMux()
	mux.Handle(prefix+"/", handler)
	if !strip {
		// Let the application produce the native bare-mount redirect.
		mux.Handle(prefix, handler)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server.URL
}

func assertAdminUIPrefixFrontend(t *testing.T, client *http.Client, frontend adminUIPrefixFrontend, publicOriginAllowed bool) {
	t.Helper()
	root := frontend.origin + frontend.mount
	for _, entry := range []struct {
		path         string
		relativeRoot string
	}{
		{path: "/", relativeRoot: "./"},
		{path: "/ui", relativeRoot: "./"},
		{path: "/ui/", relativeRoot: "../"},
		{path: "/assets/", relativeRoot: "../"},
	} {
		t.Run("index"+entry.path, func(t *testing.T) {
			page, err := url.Parse(root + entry.path + "?view=logs#details")
			require.NoError(t, err)
			response, body := getAdminUIPrefix(t, client, page.String())
			require.Equal(t, http.StatusOK, response.StatusCode)
			require.Equal(t, "text/html", adminUIPrefixMediaType(t, response))
			require.Contains(t, response.Header.Get("Cache-Control"), "no-store")

			relativeRoot, resources := adminUIPrefixIndexReferences(t, body)
			require.Equal(t, entry.relativeRoot, relativeRoot)
			rootReference, err := url.Parse(relativeRoot)
			require.NoError(t, err)
			require.Equal(t, root+"/", page.ResolveReference(rootReference).String())
			for _, resource := range resources {
				reference, err := url.Parse(resource.reference)
				require.NoError(t, err)
				require.False(t, reference.IsAbs(), "asset reference must be relative")
				require.Empty(t, reference.Host, "asset reference must not change origin")
				asset := page.ResolveReference(reference)
				require.Equal(t, page.Scheme, asset.Scheme)
				require.Equal(t, page.Host, asset.Host)
				if resource.icon {
					require.Equal(t, frontend.mount+"/favicon.ico", asset.Path)
				} else {
					require.Truef(t, strings.HasPrefix(asset.Path, frontend.mount+"/assets/"), "asset %s escaped mount %s", asset, frontend.mount)
				}
				assetResponse, assetBody := getAdminUIPrefix(t, client, asset.String())
				if resource.icon {
					require.Equal(t, http.StatusNoContent, assetResponse.StatusCode)
					continue
				}
				require.Equal(t, http.StatusOK, assetResponse.StatusCode)
				require.Contains(t, resource.mediaTypes, adminUIPrefixMediaType(t, assetResponse))
				require.Contains(t, assetResponse.Header.Get("Cache-Control"), "no-store")
				require.NotEmpty(t, assetBody)
			}
		})
	}

	for _, route := range adminUIPrefixJSONRoutes {
		response, body := getAdminUIPrefix(t, client, root+route)
		require.Equalf(t, http.StatusOK, response.StatusCode, "JSON route %s", route)
		require.Equalf(t, "application/json", adminUIPrefixMediaType(t, response), "JSON route %s", route)
		require.Truef(t, json.Valid(body), "route %s did not return JSON", route)
		if strings.Contains(route, "events?limit=") || strings.HasPrefix(route, "/api/logs?") {
			var events struct {
				Events []json.RawMessage `json:"events"`
			}
			require.NoError(t, json.Unmarshal(body, &events))
			require.LessOrEqual(t, len(events.Events), 1, "query limit must reach the backend")
		}
	}
	for _, route := range []struct{ path, body string }{{"/healthz", "live"}, {"/readyz", "ready"}, {"/metrics", "liveness"}} {
		response, body := getAdminUIPrefix(t, client, root+route.path)
		require.Equalf(t, http.StatusOK, response.StatusCode, "health route %s", route.path)
		require.Contains(t, string(body), route.body)
		response, _ = getAdminUIPrefix(t, client, root+route.path+"/extra")
		require.Equalf(t, http.StatusNotFound, response.StatusCode, "health route %s must be exact", route.path)
	}
	response, _ := getAdminUIPrefix(t, client, root+"/favicon.ico")
	require.Equal(t, http.StatusNoContent, response.StatusCode)
	// A method rejection establishes that the real Codex stream route is
	// mounted without waiting for an event or the heartbeat timer.
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, root+"/api/codex/events/stream", nil)
	require.NoError(t, err)
	response, _ = doAdminUIPrefix(t, client, request)
	require.Equal(t, http.StatusMethodNotAllowed, response.StatusCode)
	assertAdminUIPrefixExport(t, client, root)
	assertAdminUIPrefixRedirect(t, client, root+"/assets", http.StatusTemporaryRedirect, "assets/", frontend.mount+"/assets/")
	assertAdminUIPrefixRedirect(t, client, root+"/assets/index.html", http.StatusMovedPermanently, "./", frontend.mount+"/assets/")
	assertAdminUIPrefixOrigins(t, client, frontend, publicOriginAllowed)
}

type adminUIPrefixResource struct {
	reference  string
	mediaTypes []string
	icon       bool
}

func adminUIPrefixIndexReferences(t *testing.T, body []byte) (string, []adminUIPrefixResource) {
	t.Helper()
	var roots []string
	var resources []adminUIPrefixResource
	var bases, scripts, styles, icons int
	tokens := html.NewTokenizer(bytes.NewReader(body))
	for {
		tokenType := tokens.Next()
		if tokenType == html.ErrorToken {
			require.ErrorIs(t, tokens.Err(), io.EOF)
			break
		}
		if tokenType != html.StartTagToken && tokenType != html.SelfClosingTagToken {
			continue
		}
		token := tokens.Token()
		attributes := make(map[string]string, len(token.Attr))
		for _, attribute := range token.Attr {
			attributes[attribute.Key] = attribute.Val
		}
		switch token.Data {
		case "base":
			bases++
		case "meta":
			if attributes["name"] == "admin-ui-root" {
				roots = append(roots, attributes["content"])
			}
		case "script":
			if reference := attributes["src"]; reference != "" {
				scripts++
				resources = append(resources, adminUIPrefixResource{reference: reference, mediaTypes: []string{"text/javascript", "application/javascript"}})
			}
		case "link":
			relations := strings.Fields(attributes["rel"])
			if slices.Contains(relations, "stylesheet") {
				styles++
				resources = append(resources, adminUIPrefixResource{reference: attributes["href"], mediaTypes: []string{"text/css"}})
			}
			if slices.Contains(relations, "icon") {
				icons++
				resources = append(resources, adminUIPrefixResource{reference: attributes["href"], icon: true})
			}
		}
	}
	require.Zero(t, bases, "a global base element would change unrelated link resolution")
	require.Len(t, roots, 1)
	require.Positive(t, scripts)
	require.Positive(t, styles)
	require.Positive(t, icons)
	return roots[0], resources
}

func assertAdminUIPrefixExport(t *testing.T, client *http.Client, root string) {
	t.Helper()
	response, body := getAdminUIPrefix(t, client, root+"/api/logs/export?minutes=7")
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, "application/gzip", adminUIPrefixMediaType(t, response))
	disposition, parameters, err := mime.ParseMediaType(response.Header.Get("Content-Disposition"))
	require.NoError(t, err)
	require.Equal(t, "attachment", disposition)
	require.True(t, strings.HasSuffix(parameters["filename"], ".tar.gz"))
	gz, err := gzip.NewReader(bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { require.NoError(t, gz.Close()) }()
	archive := tar.NewReader(gz)
	files := make(map[string][]byte)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		files[header.Name], err = io.ReadAll(archive)
		require.NoError(t, err)
	}
	require.Contains(t, files, "tunnel-client.logs.ndjson")
	require.Contains(t, files, "tunnel-client.metrics.prom")
	require.Contains(t, files, "manifest.json")
	var manifest struct {
		Window string `json:"window"`
	}
	require.NoError(t, json.Unmarshal(files["manifest.json"], &manifest))
	require.Equal(t, "7m0s", manifest.Window, "export query must reach the backend")
}

func assertAdminUIPrefixRedirect(t *testing.T, client *http.Client, rawURL string, status int, location, resolvedPath string) {
	t.Helper()
	response, _ := getAdminUIPrefix(t, client, rawURL)
	require.Equal(t, status, response.StatusCode)
	require.Equal(t, location, response.Header.Get("Location"))
	resolved, err := response.Location()
	require.NoError(t, err)
	require.Equal(t, response.Request.URL.Scheme, resolved.Scheme)
	require.Equal(t, response.Request.URL.Host, resolved.Host)
	require.Equal(t, resolvedPath, resolved.RequestURI())
}

func assertAdminUIPrefixOrigins(t *testing.T, client *http.Client, frontend adminUIPrefixFrontend, publicOriginAllowed bool) {
	t.Helper()
	publicStatus := http.StatusForbidden
	if publicOriginAllowed {
		publicStatus = http.StatusBadRequest
	}
	for _, tc := range []struct {
		name, origin, host string
		status             int
		forwarded          bool
	}{
		{name: "local", origin: frontend.origin, status: http.StatusBadRequest},
		{name: "public-preserved-host", origin: adminUIPrefixPublicOrigin, host: "admin.example.test", status: publicStatus},
		{name: "public-rewritten-host", origin: adminUIPrefixPublicOrigin, status: publicStatus},
		{name: "foreign", origin: "https://attacker.example", host: "admin.example.test", status: http.StatusForbidden},
		{name: "forged-forwarded", origin: "https://attacker.example", host: "admin.example.test", status: http.StatusForbidden, forwarded: true},
	} {
		t.Run("origin-"+tc.name, func(t *testing.T) {
			// Invalid JSON lets an allowed request reach a harmless validation
			// error without asking the Codex fixture to start a thread.
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, frontend.origin+frontend.mount+"/api/codex/thread/start", strings.NewReader("{"))
			require.NoError(t, err)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", tc.origin)
			if tc.host != "" {
				request.Host = tc.host
			}
			if tc.forwarded {
				request.Header.Set("Forwarded", `for=127.0.0.1;host=attacker.example;proto=https`)
				request.Header.Set("X-Forwarded-For", "127.0.0.1")
				request.Header.Set("X-Forwarded-Host", "attacker.example")
				request.Header.Set("X-Forwarded-Proto", "https")
			}
			response, body := doAdminUIPrefix(t, client, request)
			require.Equal(t, tc.status, response.StatusCode)
			if tc.status == http.StatusBadRequest {
				require.Contains(t, string(body), "decode request")
			}
		})
	}
}

func getAdminUIPrefix(t *testing.T, client *http.Client, rawURL string) (*http.Response, []byte) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, rawURL, nil)
	require.NoError(t, err)
	return doAdminUIPrefix(t, client, request)
}

func doAdminUIPrefix(t *testing.T, client *http.Client, request *http.Request) (*http.Response, []byte) {
	t.Helper()
	response, err := client.Do(request)
	require.NoError(t, err)
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	require.NoError(t, readErr)
	require.NoError(t, closeErr)
	return response, body
}

func adminUIPrefixMediaType(t *testing.T, response *http.Response) string {
	t.Helper()
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	require.NoError(t, err)
	return mediaType
}
