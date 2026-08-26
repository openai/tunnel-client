package internal

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/url"
)

const maxCapturedResponseBodyBytes = 100 * 1024

type replayReadCloser struct {
	io.Reader
	io.Closer
}

// ForwardingRoundTripper decorates the base RoundTripper so it can read request
// headers from the context and capture the response headers for later use.
type ForwardingRoundTripper struct {
	base         http.RoundTripper
	serverOrigin string
}

// NewForwardingRoundTripper constructs a RoundTripper that forwards headers only
// to the configured MCP server origin.
func NewForwardingRoundTripper(base http.RoundTripper, serverURL *url.URL) http.RoundTripper {
	if base == nil {
		panic("nil base RoundTripper")
	}
	return &ForwardingRoundTripper{
		base:         base,
		serverOrigin: originKey(serverURL),
	}
}

// RoundTrip injects headers before issuing the request and records the response
// headers after the call returns.
func (f *ForwardingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, errors.New("forwarding round tripper: request is nil")
	}

	carrier := CarrierFromContext(req.Context())
	if carrier != nil {
		requestOrigin := originKey(req.URL)
		if requestOrigin != "" && requestOrigin == f.serverOrigin {
			carrier.ApplyRequestHeaders(req.Header)
		} else {
			carrier.RemoveRequestHeaders(req.Header)
		}
	}
	resp, err := f.base.RoundTrip(req)
	if err != nil {
		if carrier != nil {
			carrier.StoreTransportError(err)
		}
		return resp, err
	}
	if carrier != nil && resp != nil {
		carrier.StoreResponse(resp.StatusCode, resp.Header)
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			captureNonSuccessResponseBody(carrier, resp)
		}
	}
	return resp, nil
}

func captureNonSuccessResponseBody(carrier *HeaderCarrier, resp *http.Response) {
	if carrier == nil || resp == nil {
		return
	}
	if resp.Body == nil {
		carrier.StoreResponseBodyCapture(nil, false, nil)
		return
	}

	originalBody := resp.Body
	prefix, readErr := io.ReadAll(io.LimitReader(originalBody, maxCapturedResponseBodyBytes+1))
	resp.Body = &replayReadCloser{
		Reader: io.MultiReader(bytes.NewReader(prefix), originalBody),
		Closer: originalBody,
	}

	tooLarge := len(prefix) > maxCapturedResponseBodyBytes
	captured := prefix
	if tooLarge {
		captured = prefix[:maxCapturedResponseBodyBytes]
	}
	carrier.StoreResponseBodyCapture(captured, tooLarge, readErr)
}
