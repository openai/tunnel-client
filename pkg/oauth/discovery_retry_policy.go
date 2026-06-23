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

func classifyDiscoveryFailure(err error) discoveryFailureType {
	if err == nil {
		return discoveryFailureTypeNotApplicable
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
