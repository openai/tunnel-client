package internal

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"runtime"
	"runtime/debug"
)

// DNS diagnostics are advisory: they never select a resolver or change the
// returned error, retry policy, or health failure category. Emit only the first
// DNS failure per transport so a disconnected client does not repeat this on
// every poll. DNS errors can contain credentials, private hostnames and resolver
// addresses; only bounded classifications are safe to include here.
func (c *controlPlaneRoundTripper) logDNSFailure(ctx context.Context, err error) {
	var dnsErr *net.DNSError
	if errors.Is(err, context.Canceled) || !errors.As(err, &dnsErr) || dnsErr == nil {
		return
	}
	c.dnsDiagnosticOnce.Do(func() {
		build, ok := debug.ReadBuildInfo()
		if !ok {
			build = nil
		}
		environment := inspectDNSEnvironment(runtime.GOOS, runtime.GOARCH, build, os.Stat)
		c.logger.LogAttrs(ctx, slog.LevelWarn, "control-plane DNS resolution failed", dnsDiagnosticAttrs(dnsErr, environment)...)
	})
}

type dnsEnvironment struct {
	goos       string
	goarch     string
	cgo        string
	resolvConf string
	android    bool
}

func inspectDNSEnvironment(goos, goarch string, build *debug.BuildInfo, stat func(string) (fs.FileInfo, error)) dnsEnvironment {
	environment := dnsEnvironment{goos: goos, goarch: goarch, cgo: "unknown", resolvConf: "not_applicable", android: goos == "android"}
	if build != nil {
		for _, setting := range build.Settings {
			if setting.Key == "CGO_ENABLED" {
				switch setting.Value {
				case "0":
					environment.cgo = "disabled"
				case "1":
					environment.cgo = "enabled"
				}
			}
		}
	}
	if goos != "linux" && goos != "android" {
		return environment
	}
	if !environment.android {
		// Linux binaries can run in Termux. The Android property-service
		// directory is positive evidence; PREFIX alone is not Android detection.
		_, err := stat("/dev/__properties__")
		environment.android = err == nil
	}
	_, err := stat("/etc/resolv.conf")
	switch {
	case err == nil:
		environment.resolvConf = "present"
	case errors.Is(err, fs.ErrNotExist):
		environment.resolvConf = "missing"
	default:
		environment.resolvConf = "unavailable"
	}
	return environment
}

func dnsDiagnosticAttrs(dnsErr *net.DNSError, environment dnsEnvironment) []slog.Attr {
	failure := "other"
	switch {
	case dnsErr.IsTimeout:
		failure = "timeout"
	case dnsErr.IsNotFound:
		failure = "not_found"
	case dnsErr.IsTemporary:
		failure = "temporary"
	}
	server := "unspecified"
	if dnsErr.Server != "" {
		server = "other"
		if host, _, err := net.SplitHostPort(dnsErr.Server); err == nil && net.ParseIP(host).IsLoopback() {
			server = "loopback"
		}
	}

	hint := "Check DNS availability and the DNS policy of the active network or proxy."
	if environment.android && (environment.goos != "android" || environment.cgo == "disabled") {
		hint = "This build may not use Android's system resolver. Collect this warning and the build version; see docs/troubleshooting.md."
	} else if environment.cgo == "disabled" && environment.resolvConf == "missing" && server == "loopback" {
		hint = "This build has no cgo resolver and /etc/resolv.conf is missing. Collect this warning and the build version; see docs/troubleshooting.md."
	} else if environment.android {
		hint = "Check Android's active network, VPN and Private DNS settings; see docs/troubleshooting.md."
	}
	return []slog.Attr{
		slog.String("dns_failure", failure),
		slog.String("dns_server", server),
		slog.String("build_goos", environment.goos),
		slog.String("build_goarch", environment.goarch),
		// Build inclusion is not a claim about resolver selection: netgo,
		// GODEBUG, and custom transports can change the active resolver.
		slog.String("cgo_enabled", environment.cgo),
		slog.String("system_resolv_conf", environment.resolvConf),
		slog.Bool("android_environment", environment.android),
		slog.String("hint", hint),
	}
}
