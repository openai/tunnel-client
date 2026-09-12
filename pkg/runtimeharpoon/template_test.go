package runtimeharpoon

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/runtimeconfig"
	"github.com/openai/tunnel-client/pkg/version"
)

func templateTestConfig() *runtimeconfig.HarpoonTargetTemplate {
	return &runtimeconfig.HarpoonTargetTemplate{
		Version:      1,
		Origin:       "https://inventory.example:8443",
		Method:       http.MethodGet,
		PathTemplate: "/resource/{resourceId}",
		Parameters: map[string]runtimeconfig.HarpoonTemplateParameter{
			"resourceId": {
				Type: "string", Required: true, Pattern: `^[A-Za-z0-9_.~-]{1,64}$`, MaxLength: 64,
				ReservedValues: []string{"admin", "search"},
			},
		},
	}
}

func TestTemplateRenderIntendedOperation(t *testing.T) {
	for _, test := range []struct {
		name, path, want string
		query            map[string]string
	}{
		{name: "path identifier", path: "/resource/{resourceId}", want: "https://inventory.example:8443/resource/abc-123"},
		{name: "query identifier", path: "/search", query: map[string]string{"serialNumber": "{resourceId}"}, want: "https://inventory.example:8443/search?serialNumber=abc-123"},
		{name: "fixed query encoded separately", path: "/resource/{resourceId}", query: map[string]string{"fields": "name & status", "version": "1"}, want: "https://inventory.example:8443/resource/abc-123?fields=name+%26+status&version=1"},
		{name: "reused identifier", path: "/resource/{resourceId}", query: map[string]string{"id": "{resourceId}"}, want: "https://inventory.example:8443/resource/abc-123?id=abc-123"},
		{name: "root path", path: "/", query: map[string]string{"id": "{resourceId}"}, want: "https://inventory.example:8443/?id=abc-123"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := templateTestConfig()
			cfg.PathTemplate, cfg.Query = test.path, test.query
			template, err := CompileTargetTemplate(cfg)
			require.NoError(t, err)
			u, err := template.Render(map[string]any{"resourceId": "abc-123"})
			require.NoError(t, err)
			require.Equal(t, test.want, u.String())
			require.Equal(t, "https", u.Scheme)
			require.Equal(t, "inventory.example:8443", u.Host)
		})
	}
}

func TestTemplateMultiplePathAndQueryParameters(t *testing.T) {
	identifier := runtimeconfig.HarpoonTemplateParameter{
		Type: "string", Required: true, Pattern: `^[A-Za-z0-9_-]+$`, MaxLength: 64,
	}
	policy, err := CompileTargetTemplate(&runtimeconfig.HarpoonTargetTemplate{
		Version: 1, Origin: "https://cases.example", Method: http.MethodGet,
		PathTemplate: "/tenants/{tenant_id}/cases/{case_id}",
		Query: map[string]string{
			"view": "{view}", "requestId": "{request_id}", "archive": "false",
		},
		Parameters: map[string]runtimeconfig.HarpoonTemplateParameter{
			"tenant_id":  identifier,
			"case_id":    identifier,
			"request_id": identifier,
			"view":       {Type: "string", Required: true, Enum: []string{"summary", "details"}, MaxLength: 7},
		},
	})
	require.NoError(t, err)
	arguments := func() map[string]any {
		return map[string]any{
			"tenant_id": "tenant-1", "case_id": "CASE-123", "view": "summary", "request_id": "req-456",
		}
	}
	t.Run("renders_all_four_parameters_and_fixed_query", func(t *testing.T) {
		u, err := policy.Render(arguments())
		require.NoError(t, err)
		require.Equal(t, "https://cases.example/tenants/tenant-1/cases/CASE-123?archive=false&requestId=req-456&view=summary", u.String())
	})
	for _, parameter := range []struct{ name, invalid string }{
		{"tenant_id", "other/tenant"},
		{"case_id", ".."},
		{"view", "all"},
		{"request_id", "req-456&admin=true"},
	} {
		t.Run("missing_"+parameter.name, func(t *testing.T) {
			params := arguments()
			delete(params, parameter.name)
			u, err := policy.Render(params)
			require.Error(t, err)
			require.Nil(t, u)
		})
		t.Run("invalid_"+parameter.name, func(t *testing.T) {
			params := arguments()
			params[parameter.name] = parameter.invalid
			u, err := policy.Render(params)
			require.Error(t, err)
			require.Nil(t, u)
		})
	}
}

func TestTemplateRejectsUnsafeConfiguration(t *testing.T) {
	tests := map[string]func(*runtimeconfig.HarpoonTargetTemplate){
		"unsupported version":       func(c *runtimeconfig.HarpoonTargetTemplate) { c.Version = 2 },
		"missing version":           func(c *runtimeconfig.HarpoonTargetTemplate) { c.Version = 0 },
		"post method":               func(c *runtimeconfig.HarpoonTargetTemplate) { c.Method = "POST" },
		"missing method":            func(c *runtimeconfig.HarpoonTargetTemplate) { c.Method = "" },
		"redirects":                 func(c *runtimeconfig.HarpoonTargetTemplate) { c.FollowRedirects = true },
		"plaintext":                 func(c *runtimeconfig.HarpoonTargetTemplate) { c.Origin = "http://inventory.example" },
		"origin path":               func(c *runtimeconfig.HarpoonTargetTemplate) { c.Origin = "https://inventory.example/private" },
		"origin query":              func(c *runtimeconfig.HarpoonTargetTemplate) { c.Origin = "https://inventory.example?x=1" },
		"origin empty query":        func(c *runtimeconfig.HarpoonTargetTemplate) { c.Origin = "https://inventory.example?" },
		"origin fragment":           func(c *runtimeconfig.HarpoonTargetTemplate) { c.Origin = "https://inventory.example#private" },
		"origin empty fragment":     func(c *runtimeconfig.HarpoonTargetTemplate) { c.Origin = "https://inventory.example#" },
		"origin credentials":        func(c *runtimeconfig.HarpoonTargetTemplate) { c.Origin = "https://user:secret@inventory.example" },
		"origin placeholder":        func(c *runtimeconfig.HarpoonTargetTemplate) { c.Origin = "https://{resourceId}.example" },
		"origin no host":            func(c *runtimeconfig.HarpoonTargetTemplate) { c.Origin = "https:///" },
		"origin bad port":           func(c *runtimeconfig.HarpoonTargetTemplate) { c.Origin = "https://inventory.example:65536" },
		"origin empty port":         func(c *runtimeconfig.HarpoonTargetTemplate) { c.Origin = "https://inventory.example:" },
		"origin encoded":            func(c *runtimeconfig.HarpoonTargetTemplate) { c.Origin = "https://inventory.example/%2f" },
		"relative path":             func(c *runtimeconfig.HarpoonTargetTemplate) { c.PathTemplate = "resource/{resourceId}" },
		"path query":                func(c *runtimeconfig.HarpoonTargetTemplate) { c.PathTemplate = "/resource/{resourceId}?admin=true" },
		"path fragment":             func(c *runtimeconfig.HarpoonTargetTemplate) { c.PathTemplate = "/resource/{resourceId}#admin" },
		"path traversal":            func(c *runtimeconfig.HarpoonTargetTemplate) { c.PathTemplate = "/../resource/{resourceId}" },
		"path double slash":         func(c *runtimeconfig.HarpoonTargetTemplate) { c.PathTemplate = "/resource//{resourceId}" },
		"path encoded slash":        func(c *runtimeconfig.HarpoonTargetTemplate) { c.PathTemplate = "/resource%2f/{resourceId}" },
		"path backslash":            func(c *runtimeconfig.HarpoonTargetTemplate) { c.PathTemplate = "/resource\\/{resourceId}" },
		"partial placeholder":       func(c *runtimeconfig.HarpoonTargetTemplate) { c.PathTemplate = "/resource/prefix-{resourceId}" },
		"reserved expansion":        func(c *runtimeconfig.HarpoonTargetTemplate) { c.PathTemplate = "/resource/{+resourceId}" },
		"nested braces":             func(c *runtimeconfig.HarpoonTargetTemplate) { c.PathTemplate = "/resource/{{resourceId}}" },
		"unknown parameter":         func(c *runtimeconfig.HarpoonTargetTemplate) { c.PathTemplate = "/resource/{missing}" },
		"unused parameter":          func(c *runtimeconfig.HarpoonTargetTemplate) { c.PathTemplate = "/resource" },
		"query name placeholder":    func(c *runtimeconfig.HarpoonTargetTemplate) { c.Query = map[string]string{"{resourceId}": "fixed"} },
		"query name injection":      func(c *runtimeconfig.HarpoonTargetTemplate) { c.Query = map[string]string{"x&admin": "fixed"} },
		"partial query placeholder": func(c *runtimeconfig.HarpoonTargetTemplate) { c.Query = map[string]string{"id": "prefix-{resourceId}"} },
		"query expression":          func(c *runtimeconfig.HarpoonTargetTemplate) { c.Query = map[string]string{"id": "${resourceId}"} },
		"query control":             func(c *runtimeconfig.HarpoonTargetTemplate) { c.Query = map[string]string{"id": "a\nb"} },
		"missing parameters":        func(c *runtimeconfig.HarpoonTargetTemplate) { c.Parameters = nil },
		"invalid parameter name":    func(c *runtimeconfig.HarpoonTargetTemplate) { c.Parameters["bad-name"] = c.Parameters["resourceId"] },
		"missing bounds": func(c *runtimeconfig.HarpoonTargetTemplate) {
			p := c.Parameters["resourceId"]
			p.MaxLength = 0
			c.Parameters["resourceId"] = p
		},
		"negative minimum": func(c *runtimeconfig.HarpoonTargetTemplate) {
			p := c.Parameters["resourceId"]
			p.MinLength = -1
			c.Parameters["resourceId"] = p
		},
		"excessive bounds": func(c *runtimeconfig.HarpoonTargetTemplate) {
			p := c.Parameters["resourceId"]
			p.MaxLength = 257
			c.Parameters["resourceId"] = p
		},
		"optional parameter": func(c *runtimeconfig.HarpoonTargetTemplate) {
			p := c.Parameters["resourceId"]
			p.Required = false
			c.Parameters["resourceId"] = p
		},
		"wrong parameter type": func(c *runtimeconfig.HarpoonTargetTemplate) {
			p := c.Parameters["resourceId"]
			p.Type = "integer"
			c.Parameters["resourceId"] = p
		},
		"no format or enum": func(c *runtimeconfig.HarpoonTargetTemplate) {
			p := c.Parameters["resourceId"]
			p.Pattern = ""
			c.Parameters["resourceId"] = p
		},
		"invalid pattern": func(c *runtimeconfig.HarpoonTargetTemplate) {
			p := c.Parameters["resourceId"]
			p.Pattern = "["
			c.Parameters["resourceId"] = p
		},
		"long pattern": func(c *runtimeconfig.HarpoonTargetTemplate) {
			p := c.Parameters["resourceId"]
			p.Pattern = strings.Repeat("a", 513)
			c.Parameters["resourceId"] = p
		},
		"bad enum": func(c *runtimeconfig.HarpoonTargetTemplate) {
			p := c.Parameters["resourceId"]
			p.Enum = []string{"../admin"}
			c.Parameters["resourceId"] = p
		},
		"reserved enum": func(c *runtimeconfig.HarpoonTargetTemplate) {
			p := c.Parameters["resourceId"]
			p.Enum = []string{"ADMIN"}
			c.Parameters["resourceId"] = p
		},
		"duplicate enum": func(c *runtimeconfig.HarpoonTargetTemplate) {
			p := c.Parameters["resourceId"]
			p.Enum = []string{"one", "one"}
			c.Parameters["resourceId"] = p
		},
		"invalid reserved value": func(c *runtimeconfig.HarpoonTargetTemplate) {
			p := c.Parameters["resourceId"]
			p.ReservedValues = []string{"../admin"}
			c.Parameters["resourceId"] = p
		},
		"duplicate reserved value": func(c *runtimeconfig.HarpoonTargetTemplate) {
			p := c.Parameters["resourceId"]
			p.ReservedValues = []string{"admin", "ADMIN"}
			c.Parameters["resourceId"] = p
		},
	}
	for name, modify := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := templateTestConfig()
			modify(cfg)
			_, err := CompileTargetTemplate(cfg)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "inventory.example")
			require.NotContains(t, err.Error(), "secret")
		})
	}
}

func TestTemplateRejectsInvalidInputs(t *testing.T) {
	template, err := CompileTargetTemplate(templateTestConfig())
	require.NoError(t, err)
	for name, value := range map[string]any{
		"null": nil, "integer": 123, "float": 1.25, "bool": true,
		"array": []string{"one"}, "object": map[string]string{"id": "one"},
		"empty": "", "too long": strings.Repeat("a", 65), "traversal": "..", "dot": ".",
		"slash": "a/b", "backslash": "a\\b", "percent": "a%2Fb", "double encoded": "%252e%252e",
		"query injection": "SN1&admin=true", "fragment": "a#b", "question": "a?b", "colon": "a:b",
		"userinfo": "a@b", "newline": "a\nb", "nul": "a\x00b", "del": "a\x7fb", "space": "a b",
		"unicode": "café", "invalid utf8": "a\xffb", "reserved": "admin", "reserved case": "ADMIN",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := template.Render(map[string]any{"resourceId": value})
			require.Error(t, err)
			if raw, ok := value.(string); ok && len(raw) > 4 {
				require.NotContains(t, err.Error(), raw)
			}
		})
	}
	for _, parameters := range []map[string]any{
		nil,
		{},
		{"unknown": "one"},
		{"resourceId": "one", "unknown": "two"},
	} {
		_, err := template.Render(parameters)
		require.Error(t, err)
	}
}

func TestTemplateRegexMatchesEntireIdentifierAndEnum(t *testing.T) {
	cfg := templateTestConfig()
	p := cfg.Parameters["resourceId"]
	p.Pattern = "abc|xyz"
	cfg.Parameters["resourceId"] = p
	template, err := CompileTargetTemplate(cfg)
	require.NoError(t, err)
	for _, good := range []string{"abc", "xyz"} {
		_, err := template.Render(map[string]any{"resourceId": good})
		require.NoError(t, err)
	}
	for _, bad := range []string{"xabc", "abcx", "xyzabc"} {
		_, err := template.Render(map[string]any{"resourceId": bad})
		require.Error(t, err)
	}
	p.Pattern, p.Enum = "", []string{"one", "two"}
	cfg.Parameters["resourceId"] = p
	template, err = CompileTargetTemplate(cfg)
	require.NoError(t, err)
	_, err = template.Render(map[string]any{"resourceId": "one"})
	require.NoError(t, err)
	_, err = template.Render(map[string]any{"resourceId": "three"})
	require.Error(t, err)
}

func TestTemplatePatternsArePortableToDiscovery(t *testing.T) {
	for _, pattern := range []string{
		`(?i)abc`, `(?i:abc)`, `(?m)^abc$`, `(?P<id>abc)`, `(?<id>abc)`,
		`\A[a-z]+\z`, `\p{Greek}+`, `\P{Greek}+`, `[[:alpha:]]+`,
		`\141bc`, `\x61bc`, `\u0061bc`, `\Qabc\E`, `\babc\b`,
		`[]a]+`, `[^]]+`, `[^]a]+`,
		"café", "abc\n", `(?=abc)abc`, `(?!admin)[a-z]+`,
	} {
		t.Run(pattern, func(t *testing.T) {
			cfg := templateTestConfig()
			p := cfg.Parameters["resourceId"]
			p.Pattern = pattern
			cfg.Parameters["resourceId"] = p
			_, err := CompileTargetTemplate(cfg)
			require.Error(t, err)
		})
	}
	for _, test := range []struct{ pattern, value string }{
		{`(?:abc|xyz)`, "abc"}, {`[A-Za-z0-9_-]{1,64}`, "abc-123"},
		{`a\.b`, "a.b"}, {`\d{1,3}`, "123"}, {`\w+`, "abc_123"},
		{`[^abc]+`, "xyz"}, {`^a[bcd]?e+$`, "abeee"},
		{`[\]a]+`, "aaa"}, {`[^\]]+`, "abc"},
	} {
		t.Run(test.pattern, func(t *testing.T) {
			cfg := templateTestConfig()
			p := cfg.Parameters["resourceId"]
			p.Pattern = test.pattern
			cfg.Parameters["resourceId"] = p
			template, err := CompileTargetTemplate(cfg)
			require.NoError(t, err)
			_, err = template.Render(map[string]any{"resourceId": test.value})
			require.NoError(t, err)
		})
	}
}

func TestTemplateBounds(t *testing.T) {
	t.Run("parameter count", func(t *testing.T) {
		cfg := templateTestConfig()
		for i := 0; i < maxTemplateParameters; i++ {
			cfg.Parameters["parameter"+string(rune('A'+i))] = cfg.Parameters["resourceId"]
		}
		_, err := CompileTargetTemplate(cfg)
		require.ErrorContains(t, err, "parameters")
	})
	t.Run("query count", func(t *testing.T) {
		cfg := templateTestConfig()
		cfg.Query = make(map[string]string)
		for i := 0; i <= maxTemplateQueryKeys; i++ {
			cfg.Query["key"+string(rune('A'+i))] = "fixed"
		}
		_, err := CompileTargetTemplate(cfg)
		require.Error(t, err)
	})
	t.Run("rendered URL", func(t *testing.T) {
		cfg := templateTestConfig()
		cfg.PathTemplate = "/" + strings.Repeat("a", maxTemplateURLBytes-20) + "/{resourceId}"
		_, err := CompileTargetTemplate(cfg)
		require.ErrorContains(t, err, "URL exceeds")
	})
	t.Run("header values", func(t *testing.T) {
		cfg := templateTestConfig()
		cfg.Headers = map[string]string{"Accept": strings.Repeat("a", maxTemplateHeaderBytes)}
		_, err := CompileTargetTemplate(cfg)
		require.ErrorContains(t, err, "size limit")
	})
}

func TestTemplatePolicyHasNoMutableAliases(t *testing.T) {
	cfg := templateTestConfig()
	cfg.Headers = map[string]string{"Authorization": "Bearer fixed-secret"}
	cfg.AllowedHeaders = []string{"Accept"}
	cfg.Query = map[string]string{"version": "1"}
	p := cfg.Parameters["resourceId"]
	p.Enum = []string{"one", "two"}
	cfg.Parameters["resourceId"] = p
	template, err := CompileTargetTemplate(cfg)
	require.NoError(t, err)
	digest := template.PolicyDigest()
	cfg.Origin, cfg.PathTemplate = "https://other.example", "/other"
	cfg.Query["version"] = "2"
	cfg.Headers["Authorization"] = "Bearer changed-secret"
	cfg.AllowedHeaders[0] = "X-Other"
	p.Enum[0], p.ReservedValues[0] = "three", "one"
	delete(cfg.Parameters, "resourceId")
	public := template.PublicParameters()
	public["resourceId"].Enum[0] = "four"
	public["resourceId"].ReservedValues[0] = "one"
	delete(public, "resourceId")
	headers := template.FixedHeaders()
	headers.Set("Authorization", "Bearer changed-again")
	u, err := template.Render(map[string]any{"resourceId": "one"})
	require.NoError(t, err)
	require.Equal(t, "https://inventory.example:8443/resource/one?version=1", u.String())
	require.Equal(t, "Bearer fixed-secret", template.FixedHeaders().Get("Authorization"))
	_, err = template.ValidateCallerHeaders(map[string]string{"Accept": "application/json"})
	require.NoError(t, err)
	_, err = template.ValidateCallerHeaders(map[string]string{"X-Other": "value"})
	require.Error(t, err)
	require.Equal(t, []string{"one", "two"}, template.PublicParameters()["resourceId"].Enum)
	require.Equal(t, digest, template.PolicyDigest())
}

func TestTemplatePolicyDigestCoversPrivatePolicy(t *testing.T) {
	original, err := CompileTargetTemplate(templateTestConfig())
	require.NoError(t, err)
	require.Len(t, original.PolicyDigest(), 64)
	for name, modify := range map[string]func(*runtimeconfig.HarpoonTargetTemplate){
		"origin":      func(c *runtimeconfig.HarpoonTargetTemplate) { c.Origin = "https://other.example" },
		"path":        func(c *runtimeconfig.HarpoonTargetTemplate) { c.PathTemplate = "/other/{resourceId}" },
		"query name":  func(c *runtimeconfig.HarpoonTargetTemplate) { c.Query = map[string]string{"id": "{resourceId}"} },
		"fixed query": func(c *runtimeconfig.HarpoonTargetTemplate) { c.Query = map[string]string{"version": "2"} },
		"credential": func(c *runtimeconfig.HarpoonTargetTemplate) {
			c.Headers = map[string]string{"Authorization": "Bearer secret"}
		},
		"caller headers": func(c *runtimeconfig.HarpoonTargetTemplate) { c.AllowedHeaders = []string{"Accept"} },
		"pattern": func(c *runtimeconfig.HarpoonTargetTemplate) {
			p := c.Parameters["resourceId"]
			p.Pattern = "[a-z]+"
			c.Parameters["resourceId"] = p
		},
		"length": func(c *runtimeconfig.HarpoonTargetTemplate) {
			p := c.Parameters["resourceId"]
			p.MaxLength = 32
			c.Parameters["resourceId"] = p
		},
		"enum": func(c *runtimeconfig.HarpoonTargetTemplate) {
			p := c.Parameters["resourceId"]
			p.Enum = []string{"one", "two"}
			c.Parameters["resourceId"] = p
		},
		"reserved": func(c *runtimeconfig.HarpoonTargetTemplate) {
			p := c.Parameters["resourceId"]
			p.ReservedValues = []string{"other"}
			c.Parameters["resourceId"] = p
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := templateTestConfig()
			modify(cfg)
			changed, err := CompileTargetTemplate(cfg)
			require.NoError(t, err)
			require.NotEqual(t, original.PolicyDigest(), changed.PolicyDigest())
		})
	}
	first := templateTestConfig()
	first.Headers = map[string]string{"Authorization": "Bearer first"}
	one, err := CompileTargetTemplate(first)
	require.NoError(t, err)
	first.Headers["Authorization"] = "Bearer second"
	two, err := CompileTargetTemplate(first)
	require.NoError(t, err)
	require.NotEqual(t, one.PolicyDigest(), two.PolicyDigest())
}

func TestTemplatePolicyDigestCanonicalizesSetsAndHeaderNames(t *testing.T) {
	makeConfig := func() *runtimeconfig.HarpoonTargetTemplate {
		cfg := templateTestConfig()
		cfg.Query = map[string]string{"version": "1", "id": "{resourceId}"}
		cfg.Headers = map[string]string{"Authorization": "Bearer secret", "Content-Type": "application/json"}
		cfg.AllowedHeaders = []string{"Accept", "X-Request-Id"}
		p := cfg.Parameters["resourceId"]
		p.Enum = []string{"two", "one"}
		cfg.Parameters["resourceId"] = p
		return cfg
	}
	first, err := CompileTargetTemplate(makeConfig())
	require.NoError(t, err)
	cfg := makeConfig()
	cfg.Query = map[string]string{"id": "{resourceId}", "version": "1"}
	cfg.Headers = map[string]string{"content-type": "application/json", "authorization": "Bearer secret"}
	cfg.AllowedHeaders = []string{"x-request-id", "accept"}
	p := cfg.Parameters["resourceId"]
	p.MinLength = 1
	p.Enum = []string{"one", "two"}
	p.ReservedValues = []string{"SEARCH", "ADMIN"}
	cfg.Parameters["resourceId"] = p
	second, err := CompileTargetTemplate(cfg)
	require.NoError(t, err)
	require.Equal(t, first.PolicyDigest(), second.PolicyDigest())
}

func TestTemplateHeaderPolicy(t *testing.T) {
	for _, header := range []string{
		"Host", "Connection", "Content-Length", "Transfer-Encoding", "Cookie", "User-Agent",
		"X-Forwarded-For", "X-Forwarded-New-Identity", "X-Envoy-Original-Path", "X-Original-URL",
		"X-Rewrite-URL", "X-Auth-Request-User", "X-Authenticated-User", "X-Openai-Actor-Authorization",
		" bad", "bad header", "bad\r\nheader",
	} {
		t.Run(header, func(t *testing.T) {
			cfg := templateTestConfig()
			cfg.Headers = map[string]string{header: "value"}
			_, err := CompileTargetTemplate(cfg)
			require.Error(t, err)
			cfg.Headers = nil
			cfg.AllowedHeaders = []string{header}
			_, err = CompileTargetTemplate(cfg)
			require.Error(t, err)
		})
	}
	for name, modify := range map[string]func(*runtimeconfig.HarpoonTargetTemplate){
		"fixed casing collision": func(c *runtimeconfig.HarpoonTargetTemplate) {
			c.Headers = map[string]string{"Accept": "one", "accept": "two"}
		},
		"caller casing collision": func(c *runtimeconfig.HarpoonTargetTemplate) { c.AllowedHeaders = []string{"Accept", "accept"} },
		"fixed caller overlap": func(c *runtimeconfig.HarpoonTargetTemplate) {
			c.Headers = map[string]string{"Accept": "one"}
			c.AllowedHeaders = []string{"accept"}
		},
		"fixed value control": func(c *runtimeconfig.HarpoonTargetTemplate) {
			c.Headers = map[string]string{"Accept": "one\r\nOther: injected"}
		},
		"caller credentials": func(c *runtimeconfig.HarpoonTargetTemplate) { c.AllowedHeaders = []string{"Authorization"} },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := templateTestConfig()
			modify(cfg)
			_, err := CompileTargetTemplate(cfg)
			require.Error(t, err)
		})
	}
	cfg := templateTestConfig()
	cfg.Headers = map[string]string{"Authorization": "Bearer secret"}
	cfg.AllowedHeaders = []string{"Accept"}
	template, err := CompileTargetTemplate(cfg)
	require.NoError(t, err)
	headers, err := template.ValidateCallerHeaders(map[string]string{"accept": "application/json"})
	require.NoError(t, err)
	require.Equal(t, "application/json", headers.Get("Accept"))
	for _, caller := range []map[string]string{
		{"Authorization": "Bearer secret"}, {"authorization": "Bearer other"}, {"X-Unknown": "value"},
		{"Accept": "one", "accept": "two"}, {"Accept": "one\nInjected: true"},
		{"Accept": strings.Repeat("a", maxTemplateHeaderBytes)},
	} {
		_, err := template.ValidateCallerHeaders(caller)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "Bearer")
	}
}

func TestTemplateCallerHeadersCannotUseConcatenatedCredentialNames(t *testing.T) {
	for _, header := range []string{
		"ApiKey", "X-APIKEY", "api.key", "X-AuthToken", "x-authtoken", "X-ApiToken",
		"X-AccessToken", "X-RefreshToken", "X-IDToken", "X-SessionToken", "X-BearerToken",
		"ClientSecret", "X-ClientToken", "X-SecretKey", "X-AuthKey", "X-AuthSecret",
		"X-ApiSecret", "X-PrivateKey", "X-Authentication", "X-ClientCredential",
	} {
		t.Run(header, func(t *testing.T) {
			cfg := templateTestConfig()
			cfg.AllowedHeaders = []string{header}
			_, err := CompileTargetTemplate(cfg)
			require.ErrorContains(t, err, "authentication headers must be fixed")
			// The same header remains available to operator-owned authentication.
			cfg.AllowedHeaders = nil
			cfg.Headers = map[string]string{header: "operator-owned-secret"}
			policy, err := CompileTargetTemplate(cfg)
			require.NoError(t, err)
			_, err = policy.ValidateCallerHeaders(map[string]string{header: "caller-owned-secret"})
			require.Error(t, err)
			require.Equal(t, "operator-owned-secret", policy.FixedHeaders().Get(header))
		})
	}
	for _, header := range []string{"Accept", "Content-Type", "If-None-Match", "X-Request-Tag", "X-Request-ID", "X-Author", "X-Access-Mode", "X-Client-Name"} {
		t.Run(header, func(t *testing.T) {
			cfg := templateTestConfig()
			cfg.AllowedHeaders = []string{header}
			policy, err := CompileTargetTemplate(cfg)
			require.NoError(t, err)
			headers, err := policy.ValidateCallerHeaders(map[string]string{header: "application-value"})
			require.NoError(t, err)
			require.Equal(t, "application-value", headers.Get(header))
		})
	}
}

func TestTemplateBoundaryReauthorizesOriginalInvocation(t *testing.T) {
	cfg := templateTestConfig()
	cfg.Headers = map[string]string{"Authorization": "Bearer secret"}
	cfg.AllowedHeaders = []string{"Accept"}
	cfg.Query = map[string]string{"version": "1"}
	template, err := CompileTargetTemplate(cfg)
	require.NoError(t, err)
	params := map[string]any{"resourceId": "one"}
	u, err := template.Render(params)
	require.NoError(t, err)
	request := func(t *testing.T) *http.Request {
		req, err := http.NewRequest(http.MethodGet, u.String(), nil)
		require.NoError(t, err)
		req.Header = template.FixedHeaders()
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", version.UserAgent)
		return req
	}
	require.NoError(t, template.ValidateRequest(request(t), params))
	for name, mutate := range map[string]func(*http.Request){
		"different valid identifier": func(r *http.Request) { r.URL.Path = "/resource/two" },
		"new origin":                 func(r *http.Request) { r.URL.Host = "other.example" },
		"host override":              func(r *http.Request) { r.Host = "other.example" },
		"plaintext":                  func(r *http.Request) { r.URL.Scheme = "http" },
		"new port":                   func(r *http.Request) { r.URL.Host = "inventory.example:443" },
		"new query":                  func(r *http.Request) { r.URL.RawQuery = "version=1&admin=true" },
		"changed fixed query":        func(r *http.Request) { r.URL.RawQuery = "version=2" },
		"repeated query":             func(r *http.Request) { r.URL.RawQuery = "version=1&version=1" },
		"encoded route":              func(r *http.Request) { r.URL.RawPath = "/resource/%6fne" },
		"fragment":                   func(r *http.Request) { r.URL.Fragment = "private" },
		"credentials":                func(r *http.Request) { r.URL.User = url.UserPassword("user", "password") },
		"method":                     func(r *http.Request) { r.Method = http.MethodPost },
		"body":                       func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("body")) },
		"content length":             func(r *http.Request) { r.ContentLength = 1 },
		"transfer encoding":          func(r *http.Request) { r.TransferEncoding = []string{"chunked"} },
		"trailers":                   func(r *http.Request) { r.Trailer = http.Header{"X-Other": {"value"}} },
		"request URI":                func(r *http.Request) { r.RequestURI = "/resource/one" },
		"missing fixed auth":         func(r *http.Request) { r.Header.Del("Authorization") },
		"changed fixed auth":         func(r *http.Request) { r.Header.Set("Authorization", "Bearer other") },
		"arbitrary user agent":       func(r *http.Request) { r.Header.Set("User-Agent", "other") },
		"unknown header":             func(r *http.Request) { r.Header.Set("X-Unknown", "value") },
		"routing header":             func(r *http.Request) { r.Header.Set("X-Original-URL", "/admin") },
		"header collision":           func(r *http.Request) { r.Header["accept"] = []string{"other"} },
		"repeated header":            func(r *http.Request) { r.Header.Add("Accept", "other") },
		"header value injection":     func(r *http.Request) { r.Header.Set("Accept", "one\r\nInjected: true") },
	} {
		t.Run(name, func(t *testing.T) {
			req := request(t)
			mutate(req)
			err := template.ValidateRequest(req, params)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "inventory.example")
			require.NotContains(t, err.Error(), "Bearer")
		})
	}
	require.Error(t, template.ValidateRequest(nil, params))
}

func FuzzTemplateRenderCannotEscapeOperation(f *testing.F) {
	for _, value := range []string{"abc-123", "abc.xyz_~", "admin", ".", "..", "../admin", "%252e%252e", "SN1&admin=true", "https://other.example", "a\x00b", "café"} {
		f.Add(value)
	}
	cfg := templateTestConfig()
	cfg.Query = map[string]string{"id": "{resourceId}", "version": "1"}
	template, err := CompileTargetTemplate(cfg)
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, value string) {
		u, err := template.Render(map[string]any{"resourceId": value})
		if err != nil {
			return
		}
		require.Equal(t, "https", u.Scheme)
		require.Equal(t, "inventory.example:8443", u.Host)
		require.Nil(t, u.User)
		require.Empty(t, u.Fragment)
		require.Equal(t, []string{"", "resource", value}, strings.Split(u.Path, "/"))
		require.Equal(t, url.Values{"id": {value}, "version": {"1"}}, u.Query())
		require.LessOrEqual(t, len(u.String()), maxTemplateURLBytes)
	})
}
