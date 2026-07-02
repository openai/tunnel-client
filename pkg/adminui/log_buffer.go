package adminui

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultLogCapacity   = 2000
	defaultSubscriberBuf = 256
)

var (
	// Redact common OpenAI-style API keys and Bearer tokens that can appear in raw dumps.
	reBearerToken = regexp.MustCompile(`(?i)(Authorization:\s*Bearer\s+)([^\r\n]+)`)
	reShardToken  = regexp.MustCompile(`(?i)(X-Tunnel-Shard-Token:\s*)([^\r\n]+)`)
	reCookie      = regexp.MustCompile(`(?i)(Cookie:\s*)([^\r\n]+)`)
	reSetCookie   = regexp.MustCompile(`(?i)(Set-Cookie:\s*)([^\r\n]+)`)
	reSkKey       = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{10,}\b`)
	reQuerySecret = regexp.MustCompile(`(?i)([?&](?:api[_-]?key|access_token|refresh_token|id_token|client_secret|code|password|secret)=)([^&#\s]+)`)
	reFormSecret  = regexp.MustCompile(`(?i)\b((?:api[_-]?key|access_token|refresh_token|id_token|client_secret|code|password|secret)\s*=\s*)([^&\s]+)`)
	reJSONSecret  = regexp.MustCompile(`(?i)(\"(?:api[_-]?key|access_token|refresh_token|id_token|client_secret|code|password|secret)\"\s*:\s*)(\"(?:\\.|[^\"])*\"|[^\s,\}\]]+)`)
	reURLUserInfo = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)([^/@\s:]+):([^/@\s]+)@`)
)

// LogEvent is the structured representation exposed by the admin UI.
type LogEvent struct {
	Seq     int64          `json:"seq"`
	Time    time.Time      `json:"time"`
	Level   string         `json:"level"`
	Message string         `json:"message"`
	Attrs   map[string]any `json:"attrs,omitempty"`
}

// LogBuffer stores the most recent log events in memory and supports fan-out streaming.
//
// It is intentionally simple: a fixed-size ring plus a best-effort broadcast
// to subscribers (slow subscribers will drop events).
type LogBuffer struct {
	startedAt time.Time

	capacity int
	ring     []LogEvent
	start    int
	size     int

	nextSeq atomic.Int64

	mu        sync.Mutex
	subs      map[int]chan LogEvent
	nextSubID int
}

func NewLogBuffer() *LogBuffer {
	return NewLogBufferWithCapacity(defaultLogCapacity)
}

func NewLogBufferWithCapacity(capacity int) *LogBuffer {
	if capacity <= 0 {
		capacity = defaultLogCapacity
	}
	return &LogBuffer{
		startedAt: time.Now(),
		capacity:  capacity,
		ring:      make([]LogEvent, capacity),
		subs:      make(map[int]chan LogEvent),
	}
}

func (b *LogBuffer) StartedAt() time.Time {
	if b == nil {
		return time.Time{}
	}
	return b.startedAt
}

func (b *LogBuffer) Capacity() int {
	if b == nil {
		return 0
	}
	return b.capacity
}

// Handle implements go.openai.org/api/tunnel-client/pkg/log.Sink.
func (b *LogBuffer) Handle(ctx context.Context, record slog.Record) {
	if b == nil {
		return
	}

	seq := b.nextSeq.Add(1)
	ev := LogEvent{
		Seq:     seq,
		Time:    record.Time,
		Level:   record.Level.String(),
		Message: redactString(record.Message),
	}

	attrs := make(map[string]any, record.NumAttrs())
	record.Attrs(func(a slog.Attr) bool {
		addAttr(attrs, a)
		return true
	})
	if len(attrs) > 0 {
		ev.Attrs = attrs
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.appendLocked(ev)
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
			// Best-effort: never block logging.
		}
	}
}

func (b *LogBuffer) Recent(limit int) []LogEvent {
	if b == nil {
		return nil
	}
	if limit <= 0 {
		limit = 200
	}
	if limit > b.capacity {
		limit = b.capacity
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.size == 0 {
		return nil
	}

	if limit > b.size {
		limit = b.size
	}

	out := make([]LogEvent, 0, limit)
	start := b.start
	// Oldest is at b.start; we want the last `limit` entries.
	first := (start + b.size - limit) % b.capacity
	for i := 0; i < limit; i++ {
		idx := (first + i) % b.capacity
		out = append(out, b.ring[idx])
	}
	return out
}

func (b *LogBuffer) Since(since time.Time, limit int) []LogEvent {
	if b == nil {
		return nil
	}
	events := b.Recent(limit)
	if since.IsZero() {
		return events
	}
	out := make([]LogEvent, 0, len(events))
	for _, ev := range events {
		if ev.Time.IsZero() || ev.Time.Before(since) {
			continue
		}
		out = append(out, ev)
	}
	return out
}

func (b *LogBuffer) Subscribe(ctx context.Context) <-chan LogEvent {
	if b == nil {
		ch := make(chan LogEvent)
		close(ch)
		return ch
	}
	if ctx == nil {
		ctx = context.Background()
	}

	ch := make(chan LogEvent, defaultSubscriberBuf)

	b.mu.Lock()
	id := b.nextSubID
	b.nextSubID++
	b.subs[id] = ch
	b.mu.Unlock()

	go func() {
		<-ctx.Done()
		b.mu.Lock()
		if sub, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(sub)
		}
		b.mu.Unlock()
	}()

	return ch
}

func (b *LogBuffer) appendLocked(ev LogEvent) {
	if b.capacity <= 0 {
		return
	}
	if b.size < b.capacity {
		idx := (b.start + b.size) % b.capacity
		b.ring[idx] = ev
		b.size++
		return
	}
	// Overwrite oldest.
	b.ring[b.start] = ev
	b.start = (b.start + 1) % b.capacity
}

func addAttr(dst map[string]any, attr slog.Attr) {
	if dst == nil {
		return
	}
	attr.Value = attr.Value.Resolve()

	if attr.Key == "" {
		return
	}

	if isSensitiveAttrKey(attr.Key) {
		dst[attr.Key] = "[REDACTED]"
		return
	}

	switch attr.Value.Kind() {
	case slog.KindGroup:
		group := make(map[string]any)
		for _, child := range attr.Value.Group() {
			addAttr(group, child)
		}
		if len(group) > 0 {
			dst[attr.Key] = group
		}
	default:
		dst[attr.Key] = redactAny(valueToAny(attr.Value))
	}
}

func valueToAny(v slog.Value) any {
	v = v.Resolve()
	switch v.Kind() {
	case slog.KindBool:
		return v.Bool()
	case slog.KindDuration:
		return v.Duration().String()
	case slog.KindFloat64:
		return v.Float64()
	case slog.KindInt64:
		return v.Int64()
	case slog.KindString:
		return v.String()
	case slog.KindTime:
		return v.Time()
	case slog.KindUint64:
		return v.Uint64()
	case slog.KindAny:
		raw := v.Any()
		switch t := raw.(type) {
		case error:
			return t.Error()
		case fmt.Stringer:
			return t.String()
		default:
			return raw
		}
	default:
		return v.String()
	}
}

func redactAny(v any) any {
	switch t := v.(type) {
	case string:
		return redactString(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			if isSensitiveAttrKey(k) {
				out[k] = "[REDACTED]"
			} else {
				out[k] = redactAny(vv)
			}
		}
		return out
	case []any:
		out := make([]any, 0, len(t))
		for _, vv := range t {
			out = append(out, redactAny(vv))
		}
		return out
	default:
		return redactReflectValue(reflect.ValueOf(v), v)
	}
}

func redactReflectValue(rv reflect.Value, fallback any) any {
	if !rv.IsValid() {
		return fallback
	}
	switch rv.Kind() {
	case reflect.Interface, reflect.Pointer:
		if rv.IsNil() {
			return fallback
		}
		return redactReflectValue(rv.Elem(), rv.Elem().Interface())
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return fallback
		}
		out := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			key := iter.Key().String()
			if isSensitiveAttrKey(key) {
				out[key] = "[REDACTED]"
				continue
			}
			out[key] = redactReflectValue(iter.Value(), iter.Value().Interface())
		}
		return out
	case reflect.Slice, reflect.Array:
		out := make([]any, 0, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			item := rv.Index(i)
			out = append(out, redactReflectValue(item, item.Interface()))
		}
		return out
	case reflect.String:
		return redactString(rv.String())
	default:
		return fallback
	}
}

func redactString(s string) string {
	if s == "" {
		return s
	}
	s = reBearerToken.ReplaceAllString(s, `${1}[REDACTED]`)
	s = reShardToken.ReplaceAllString(s, `${1}[REDACTED]`)
	s = reCookie.ReplaceAllString(s, `${1}[REDACTED]`)
	s = reSetCookie.ReplaceAllString(s, `${1}[REDACTED]`)
	s = reSkKey.ReplaceAllString(s, `sk-REDACTED`)
	s = reQuerySecret.ReplaceAllString(s, `${1}[REDACTED]`)
	s = reFormSecret.ReplaceAllString(s, `${1}[REDACTED]`)
	s = reJSONSecret.ReplaceAllString(s, `${1}"[REDACTED]"`)
	s = reURLUserInfo.ReplaceAllString(s, `${1}[REDACTED]@`)
	return s
}

func isSensitiveAttrKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	switch normalized {
	case "authorization",
		"cookie",
		"set_cookie",
		"x_tunnel_shard_token",
		"shard_token",
		"api_key",
		"apikey",
		"access_token",
		"refresh_token",
		"id_token",
		"client_secret",
		"password",
		"secret":
		return true
	default:
		return false
	}
}
