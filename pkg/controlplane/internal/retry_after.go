package internal

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxRetryAfterDelay = time.Minute

// parseRetryAfter parses the standard Retry-After header into a relative
// delay. It accepts both delta-seconds and HTTP-date forms, ignores invalid or
// expired values, and caps valid server hints so one response cannot stall the
// client indefinitely.
func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}

	if isDecimalDigits(value) {
		seconds, err := strconv.ParseUint(value, 10, 64)
		if err != nil || seconds > uint64(maxRetryAfterDelay/time.Second) {
			return maxRetryAfterDelay, true
		}
		return time.Duration(seconds) * time.Second, true
	}

	retryAt, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	delay := retryAt.Sub(now)
	if delay <= 0 {
		return 0, false
	}
	if delay > maxRetryAfterDelay {
		return maxRetryAfterDelay, true
	}
	return delay, true
}

func isDecimalDigits(value string) bool {
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return value != ""
}

func retryDelay(backoffDelay time.Duration, retryAfter time.Duration, hasRetryAfter bool) time.Duration {
	if hasRetryAfter && retryAfter > backoffDelay {
		return retryAfter
	}
	return backoffDelay
}

func sleepWithContext(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
