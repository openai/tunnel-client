package hostclassifier

import (
	"net/url"
	"testing"

	"go.openai.org/api/tunnel-client/pkg/config"
)

func TestHostClassifierDefaultsPrivateIPs(t *testing.T) {
	classifier := NewHostClassifier(config.HarpoonHostClassifierConfig{
		IncludeLoopback: true,
		IncludePrivate:  true,
	})

	cases := []string{
		"127.0.0.1",
		"::1",
		"10.0.0.1",
		"172.16.5.4",
		"192.168.1.10",
	}

	for _, host := range cases {
		if ok, _ := classifier.IsPrivateHost(host); !ok {
			t.Fatalf("expected host %q to be private", host)
		}
	}
}

func TestHostClassifierSuffixAndRegex(t *testing.T) {
	classifier := NewHostClassifier(config.HarpoonHostClassifierConfig{
		IncludeLoopback: false,
		IncludePrivate:  false,
		IncludeSuffix:   []string{".internal", "corp"},
		IncludeRegex:    []string{`^svc-[a-z]+\.example\.com$`},
	})

	if ok, _ := classifier.IsPrivateHost("api.internal"); !ok {
		t.Fatal("expected suffix match for api.internal")
	}
	if ok, _ := classifier.IsPrivateHost("internal"); !ok {
		t.Fatal("expected suffix match for internal")
	}
	if ok, _ := classifier.IsPrivateHost("foo.corp"); !ok {
		t.Fatal("expected suffix match for foo.corp")
	}
	if ok, _ := classifier.IsPrivateHost("SVC-Alpha.Example.Com"); !ok {
		t.Fatal("expected regex match to be case-insensitive")
	}
	if ok, _ := classifier.IsPrivateHost("public.example.com"); ok {
		t.Fatal("unexpected private match")
	}
}

func TestHostClassifierDisableRanges(t *testing.T) {
	classifier := NewHostClassifier(config.HarpoonHostClassifierConfig{
		IncludeLoopback: false,
		IncludePrivate:  false,
	})

	if ok, _ := classifier.IsPrivateHost("127.0.0.1"); ok {
		t.Fatal("expected loopback to be excluded")
	}
	if ok, _ := classifier.IsPrivateHost("10.0.0.1"); ok {
		t.Fatal("expected private IPs to be excluded")
	}
}

func TestHostClassifierURLHostname(t *testing.T) {
	classifier := NewHostClassifier(config.HarpoonHostClassifierConfig{
		IncludeSuffix:  []string{"internal"},
		IncludePrivate: false,
	})

	parsed, err := url.Parse("https://api.internal:8443/v1")
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}

	if ok, _ := classifier.IsPrivateURL(parsed); !ok {
		t.Fatal("expected URL host to be private")
	}
}

func TestHostClassifierRejectsUnsupportedScheme(t *testing.T) {
	classifier := NewHostClassifier(config.HarpoonHostClassifierConfig{
		IncludeSuffix: []string{"internal"},
	})

	parsed, err := url.Parse("api://resource.internal")
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}

	ok, reason := classifier.IsPrivateURL(parsed)
	if ok {
		t.Fatal("expected unsupported scheme to be rejected")
	}
	if reason == "" {
		t.Fatal("expected reason for unsupported scheme")
	}
}
