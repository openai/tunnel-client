package oauth

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

const (
	oauthMetadataRequestRetryCount          = 3
	oauthMetadataRequestTimeoutBase         = 2 * time.Second
	protectedResourceMetadataBodyLimitBytes = 1024 * 1024 // 1MB
	authServerMetadataBodyLimitBytes        = 1024 * 1024 // 1MB
)

type discoveryRetryMode int

const (
	discoveryRetryModeNone discoveryRetryMode = iota
	discoveryRetryModeTimeoutBackoff
)

type discoveryFailureType int

const (
	discoveryFailureTypeNotApplicable discoveryFailureType = iota
	discoveryFailureTypeTimeoutOnly
	discoveryFailureTypeNonTimeout
	discoveryFailureTypeRetryableResponse
)

// discoveryResponseFailures distinguishes a temporarily unusable metadata
// response from permanent request/configuration failures. Missing fallback URLs
// do not veto recovery of another endpoint, but trust or non-timeout transport
// failures do.
type discoveryResponseFailures struct {
	retryable bool
	permanent bool
	timeout   bool
}

func (f *discoveryResponseFailures) record(status int, err error, invalidBody bool) {
	if classifyDiscoveryFailure(err) == discoveryFailureTypeTimeoutOnly {
		f.timeout = true
		return
	}
	if status == http.StatusNotFound {
		return
	}
	if isRetryableDiscoveryResponseStatus(status) ||
		(invalidBody && status >= 200 && status < 300) {
		f.retryable = true
		return
	}
	f.permanent = true
}

func isRetryableDiscoveryResponseStatus(status int) bool {
	return status == http.StatusTooManyRequests || (status >= 500 && status <= 599)
}

func (f discoveryResponseFailures) classify(fallback discoveryFailureType) discoveryFailureType {
	if f.permanent {
		return discoveryFailureTypeNonTimeout
	}
	if f.retryable {
		return discoveryFailureTypeRetryableResponse
	}
	if f.timeout {
		return discoveryFailureTypeTimeoutOnly
	}
	return fallback
}

// classifiedDiscoveryError preserves the complete discovery cycle's retry
// classification while retaining the final error's text and errors.Is/As chain.
type classifiedDiscoveryError struct {
	err         error
	failureType discoveryFailureType
}

func (e *classifiedDiscoveryError) Error() string { return e.err.Error() }

func (e *classifiedDiscoveryError) Unwrap() error { return e.err }

func withDiscoveryFailureType(err error, failureType discoveryFailureType) error {
	if err == nil {
		return nil
	}
	if failureType == discoveryFailureTypeNotApplicable {
		failureType = discoveryFailureTypeNonTimeout
	}
	return &classifiedDiscoveryError{err: err, failureType: failureType}
}

func classifyDiscoveryFailure(err error) discoveryFailureType {
	if err == nil {
		return discoveryFailureTypeNotApplicable
	}
	if classified, ok := errors.AsType[*classifiedDiscoveryError](err); ok {
		return classified.failureType
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return discoveryFailureTypeTimeoutOnly
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return discoveryFailureTypeTimeoutOnly
	}
	return discoveryFailureTypeNonTimeout
}
