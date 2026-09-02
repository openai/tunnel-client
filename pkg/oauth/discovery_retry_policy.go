package oauth

import (
	"context"
	"errors"
	"net"
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
)

// classifiedDiscoveryError preserves whether every candidate in a discovery
// cycle timed out while retaining the final error's text and errors.Is/As chain.
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
	var classified *classifiedDiscoveryError
	if errors.As(err, &classified) {
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
