package internal

import (
	"context"
	"errors"
	"net"
)

// healthFailure deliberately exposes categories rather than error messages,
// which can contain response bodies, URLs, headers, or credential material.
func healthFailure(err error) (string, int) {
	if err == nil {
		return "", 0
	}
	var status *APIStatusError
	if errors.As(err, &status) {
		return "http_error", status.StatusCode()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout", 0
	}
	if errors.Is(err, context.Canceled) {
		return "canceled", 0
	}
	var network net.Error
	if errors.As(err, &network) {
		if network.Timeout() {
			return "timeout", 0
		}
		return "network_error", 0
	}
	return "request_error", 0
}
