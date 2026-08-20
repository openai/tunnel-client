package adminui

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLogBufferRecentAndOverwrite(t *testing.T) {
	t.Parallel()

	b := NewLogBufferWithCapacity(3)

	emit := func(msg string) {
		r := slog.NewRecord(time.Now(), slog.LevelInfo, msg, 0)
		b.Handle(context.Background(), r)
	}

	emit("one")
	emit("two")
	emit("three")

	got := b.Recent(10)
	require.Len(t, got, 3)
	require.Equal(t, "one", got[0].Message)
	require.Equal(t, "two", got[1].Message)
	require.Equal(t, "three", got[2].Message)

	emit("four")
	got = b.Recent(10)
	require.Len(t, got, 3)
	require.Equal(t, "two", got[0].Message)
	require.Equal(t, "three", got[1].Message)
	require.Equal(t, "four", got[2].Message)
}

func TestLogBufferRedactsBearerToken(t *testing.T) {
	t.Parallel()

	b := NewLogBufferWithCapacity(10)
	r := slog.NewRecord(time.Now(), slog.LevelInfo, "raw http request", 0)
	r.AddAttrs(slog.String("dump", "Authorization: Bearer sk-proj-abcdefg1234567890\r\n"))
	b.Handle(context.Background(), r)

	got := b.Recent(1)
	require.Len(t, got, 1)
	require.Contains(t, got[0].Attrs["dump"], "Authorization: Bearer [REDACTED]")
	require.NotContains(t, got[0].Attrs["dump"], "sk-proj-")
}

func TestLogBufferRedactsSensitiveAttrKeysAndQueryParams(t *testing.T) {
	t.Parallel()

	b := NewLogBufferWithCapacity(10)
	r := slog.NewRecord(time.Now(), slog.LevelInfo, "oauth callback https://example.test/cb?code=abc123&state=ok", 0)
	r.AddAttrs(
		slog.String("Authorization", "Bearer sk-proj-abcdefg1234567890"),
		slog.Any("headers", map[string]any{
			"Cookie":      "session=secret-cookie",
			"ContentType": "application/json",
		}),
		slog.String("safe_url", "https://example.test/token?client_secret=s3cr3t&scope=mcp"),
	)
	b.Handle(context.Background(), r)

	got := b.Recent(1)
	require.Len(t, got, 1)
	require.Equal(t, "[REDACTED]", got[0].Attrs["Authorization"])
	require.Equal(t, "[REDACTED]", got[0].Attrs["headers"].(map[string]any)["Cookie"])
	require.Contains(t, got[0].Message, "code=[REDACTED]")
	require.Contains(t, got[0].Attrs["safe_url"], "client_secret=[REDACTED]")
	require.NotContains(t, got[0].Message, "abc123")
	require.NotContains(t, got[0].Attrs["safe_url"], "s3cr3t")
}

func TestLogBufferRedactsJSONAndFormSecretsInRawDump(t *testing.T) {
	t.Parallel()

	b := NewLogBufferWithCapacity(10)
	r := slog.NewRecord(time.Now(), slog.LevelDebug, "raw http request", 0)
	r.AddAttrs(slog.String("dump", "POST /token HTTP/1.1\r\nContent-Type: application/json\r\n\r\n{\"access_token\":\"opaque-token\",\"password\":\"very-secret\"}\r\n\r\nclient_secret=another-secret&scope=openid"))
	b.Handle(context.Background(), r)

	got := b.Recent(1)
	require.Len(t, got, 1)
	require.Contains(t, got[0].Attrs["dump"], `"access_token":"[REDACTED]"`)
	require.Contains(t, got[0].Attrs["dump"], `"password":"[REDACTED]"`)
	require.Contains(t, got[0].Attrs["dump"], "client_secret=[REDACTED]")
	require.NotContains(t, got[0].Attrs["dump"], "opaque-token")
	require.NotContains(t, got[0].Attrs["dump"], "very-secret")
	require.NotContains(t, got[0].Attrs["dump"], "another-secret")
}

func TestLogBufferRedactsNamedMapAndSliceValues(t *testing.T) {
	t.Parallel()

	type headerMap map[string][]string
	type oauthAttrs map[string]any

	b := NewLogBufferWithCapacity(10)
	r := slog.NewRecord(time.Now(), slog.LevelInfo, "structured request", 0)
	r.AddAttrs(
		slog.Any("headers", headerMap{
			"Authorization": {"Bearer sk-proj-secretsecret123456"},
			"X-Trace":       {"safe"},
		}),
		slog.Any("oauth", oauthAttrs{
			"callback": "https://client.example/cb?code=oauth-secret-code&state=ok",
			"nested":   []any{"Authorization: Bearer sk-proj-nested123456"},
		}),
	)
	b.Handle(context.Background(), r)

	got := b.Recent(1)
	require.Len(t, got, 1)
	gotHeaders, ok := got[0].Attrs["headers"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "[REDACTED]", gotHeaders["Authorization"])
	require.Equal(t, []any{"safe"}, gotHeaders["X-Trace"])
	gotOAuth, ok := got[0].Attrs["oauth"].(map[string]any)
	require.True(t, ok)
	require.Contains(t, gotOAuth["callback"], "code=[REDACTED]")
	require.Equal(t, []any{"Authorization: Bearer [REDACTED]"}, gotOAuth["nested"])
}

func TestLogBufferSinceFiltersByTimestamp(t *testing.T) {
	t.Parallel()

	b := NewLogBufferWithCapacity(10)
	now := time.Now()
	for _, tc := range []struct {
		t   time.Time
		msg string
	}{
		{t: now.Add(-31 * time.Minute), msg: "old"},
		{t: now.Add(-30 * time.Minute), msg: "edge"},
		{t: now.Add(-1 * time.Minute), msg: "new"},
	} {
		r := slog.NewRecord(tc.t, slog.LevelInfo, tc.msg, 0)
		b.Handle(context.Background(), r)
	}

	got := b.Since(now.Add(-30*time.Minute), 10)
	require.Len(t, got, 2)
	require.Equal(t, "edge", got[0].Message)
	require.Equal(t, "new", got[1].Message)
}

func TestLogBufferSubscribeIsBestEffort(t *testing.T) {
	t.Parallel()

	b := NewLogBufferWithCapacity(10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := b.Subscribe(ctx)
	require.NotNil(t, ch)

	r := slog.NewRecord(time.Now(), slog.LevelInfo, "hello", 0)
	b.Handle(ctx, r)

	select {
	case ev := <-ch:
		require.Equal(t, "hello", ev.Message)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for subscribed log event")
	}
}
