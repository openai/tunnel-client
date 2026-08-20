package internal

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestForwardingRoundTripperRoundTrip(t *testing.T) {
	makeResponse := func(status int, headers http.Header) *http.Response {
		return &http.Response{
			StatusCode: status,
			Header:     headers.Clone(),
			Body:       io.NopCloser(strings.NewReader("")),
		}
	}

	tests := []struct {
		name                string
		requestHeaders      http.Header
		expectRequestHeader http.Header
		baseResponse        *http.Response
		baseError           error
		expectedStatus      int
		expectedHeaders     http.Header
		preexistingHeaders  http.Header
	}{
		{
			name:                "injects request headers and captures response",
			requestHeaders:      http.Header{"X-Test": {"forward-me"}, "X-Other": {"one", "two"}},
			expectRequestHeader: http.Header{"X-Test": {"forward-me"}, "X-Other": {"one", "two"}},
			preexistingHeaders:  http.Header{"X-Test": {"should-be-overwritten"}},
			baseResponse:        makeResponse(http.StatusAccepted, http.Header{"X-Resp": {"ok"}}),
			expectedStatus:      http.StatusAccepted,
			expectedHeaders:     http.Header{"X-Resp": {"ok"}},
		},
		{
			name:            "captures response headers",
			requestHeaders:  nil,
			baseResponse:    makeResponse(http.StatusOK, http.Header{"X-Resp": {"ok"}, "X-Trace": {"abc"}}),
			expectedStatus:  http.StatusOK,
			expectedHeaders: http.Header{"X-Resp": {"ok"}, "X-Trace": {"abc"}},
		},
		{
			name:            "propagates error without capturing response",
			requestHeaders:  http.Header{"X-Test": {"forward-me"}},
			baseResponse:    makeResponse(http.StatusBadGateway, http.Header{"X-Resp": {"should-not-store"}}),
			baseError:       errors.New("transport failure"),
			expectedStatus:  0,
			expectedHeaders: nil,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ctx, carrier, err := ContextWithHeaders(context.Background(), tc.requestHeaders)
			if err != nil {
				t.Fatalf("ContextWithHeaders: %v", err)
			}

			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.com", nil)
			if err != nil {
				t.Fatalf("NewRequestWithContext: %v", err)
			}
			for key, values := range tc.preexistingHeaders {
				for _, value := range values {
					req.Header.Add(key, value)
				}
			}

			rt := NewForwardingRoundTripper(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				for key, want := range tc.expectRequestHeader {
					got := req.Header.Values(key)
					if diff := cmp.Diff(want, got, cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
						t.Fatalf("request header %s mismatch (-want +got):\n%s", key, diff)
					}
				}
				return tc.baseResponse, tc.baseError
			}))

			resp, err := rt.RoundTrip(req)
			if tc.baseError != nil {
				if !errors.Is(err, tc.baseError) {
					t.Fatalf("RoundTrip error mismatch: got %v, want %v", err, tc.baseError)
				}
			} else if err != nil {
				t.Fatalf("RoundTrip: %v", err)
			}

			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}

			status, headers := carrier.ResponseStatusAndHeaders()
			if status != tc.expectedStatus {
				t.Fatalf("response status mismatch: got %d, want %d", status, tc.expectedStatus)
			}
			if diff := cmp.Diff(tc.expectedHeaders, headers, cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
				t.Fatalf("response headers mismatch (-want +got):\n%s", diff)
			}
			if got := carrier.TransportError(); !errors.Is(got, tc.baseError) {
				t.Fatalf("transport error mismatch: got %v, want %v", got, tc.baseError)
			}
		})
	}
}

func TestNewForwardingRoundTripperPanicsOnNil(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic when base RoundTripper is nil")
		}
	}()

	_ = NewForwardingRoundTripper(nil)
}

func TestForwardingRoundTripperCapturesAndReplaysNonSuccessBody(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		body         []byte
		wantTooLarge bool
	}{
		{
			name: "bounded",
			body: []byte(`{"jsonrpc":"2.0","id":"request","error":{"code":-32003,"message":"capability"}}`),
		},
		{
			name:         "oversized",
			body:         bytes.Repeat([]byte("x"), maxCapturedResponseBodyBytes+17),
			wantTooLarge: true,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			originalBody := &trackingReadCloser{Reader: bytes.NewReader(tc.body)}
			ctx, carrier, err := ContextWithHeaders(context.Background(), nil)
			if err != nil {
				t.Fatalf("ContextWithHeaders: %v", err)
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://example.com/mcp", nil)
			if err != nil {
				t.Fatalf("NewRequestWithContext: %v", err)
			}

			rt := NewForwardingRoundTripper(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Header:     http.Header{"Content-Type": {"application/json"}},
					Body:       originalBody,
				}, nil
			}))
			resp, err := rt.RoundTrip(req)
			if err != nil {
				t.Fatalf("RoundTrip: %v", err)
			}
			gotBody, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read replayed body: %v", err)
			}
			if !bytes.Equal(tc.body, gotBody) {
				t.Fatalf("replayed body mismatch: got %d bytes, want %d", len(gotBody), len(tc.body))
			}
			if err := resp.Body.Close(); err != nil {
				t.Fatalf("close replayed body: %v", err)
			}
			if !originalBody.closed {
				t.Fatal("closing replay wrapper did not close original body")
			}

			capturedBody, tooLarge, readErr, captured := carrier.ResponseBodyCapture()
			if !captured {
				t.Fatal("non-success response body was not captured")
			}
			if readErr != nil {
				t.Fatalf("unexpected capture read error: %v", readErr)
			}
			if tooLarge != tc.wantTooLarge {
				t.Fatalf("tooLarge mismatch: got %v want %v", tooLarge, tc.wantTooLarge)
			}
			wantCapture := tc.body
			if len(wantCapture) > maxCapturedResponseBodyBytes {
				wantCapture = wantCapture[:maxCapturedResponseBodyBytes]
			}
			if !bytes.Equal(wantCapture, capturedBody) {
				t.Fatalf("captured body mismatch: got %d bytes, want %d", len(capturedBody), len(wantCapture))
			}
		})
	}
}

func TestForwardingRoundTripperCapturesBodyReadErrorWithoutLosingPrefix(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("read failed")
	originalBody := &oneShotErrorReadCloser{data: []byte("partial"), err: wantErr}
	ctx, carrier, err := ContextWithHeaders(context.Background(), nil)
	if err != nil {
		t.Fatalf("ContextWithHeaders: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://example.com/mcp", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	rt := NewForwardingRoundTripper(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadGateway, Header: http.Header{}, Body: originalBody}, nil
	}))
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	replayed, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read replayed prefix: %v", err)
	}
	if string(replayed) != "partial" {
		t.Fatalf("replayed prefix mismatch: got %q", replayed)
	}
	_ = resp.Body.Close()

	capturedBody, tooLarge, readErr, captured := carrier.ResponseBodyCapture()
	if !captured || tooLarge || !errors.Is(readErr, wantErr) || string(capturedBody) != "partial" {
		t.Fatalf("unexpected capture: body=%q tooLarge=%v readErr=%v captured=%v", capturedBody, tooLarge, readErr, captured)
	}
}

func TestForwardingRoundTripperDoesNotCaptureSuccessBody(t *testing.T) {
	t.Parallel()

	ctx, carrier, err := ContextWithHeaders(context.Background(), nil)
	if err != nil {
		t.Fatalf("ContextWithHeaders: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://example.com/mcp", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	rt := NewForwardingRoundTripper(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0"}`)),
		}, nil
	}))
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()
	if body, tooLarge, readErr, captured := carrier.ResponseBodyCapture(); captured || body != nil || tooLarge || readErr != nil {
		t.Fatalf("success response unexpectedly captured: body=%q tooLarge=%v readErr=%v captured=%v", body, tooLarge, readErr, captured)
	}
}

func TestForwardingRoundTripperRoundTripRejectsNilRequest(t *testing.T) {
	t.Parallel()

	rt := NewForwardingRoundTripper(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("base round tripper should not be called for nil request")
		return nil, nil
	}))

	resp, err := rt.RoundTrip(nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
	if resp != nil {
		t.Fatalf("expected nil response for nil request, got %v", resp)
	}
	if !strings.Contains(err.Error(), "request is nil") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestForwardingRoundTripperRoundTripToleratesNilResponseWithoutError(t *testing.T) {
	t.Parallel()

	ctx, carrier, err := ContextWithHeaders(context.Background(), http.Header{"X-Test": {"forward-me"}})
	if err != nil {
		t.Fatalf("ContextWithHeaders: %v", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.com", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}

	baseCalled := false
	rt := NewForwardingRoundTripper(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		baseCalled = true
		return nil, nil
	}))

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip returned unexpected error: %v", err)
	}
	if resp != nil {
		t.Fatalf("expected nil response from base transport, got %#v", resp)
	}
	if !baseCalled {
		t.Fatal("expected base round tripper to be called")
	}

	status, headers := carrier.ResponseStatusAndHeaders()
	if status != 0 {
		t.Fatalf("expected zero response status when response is nil, got %d", status)
	}
	if headers != nil {
		t.Fatalf("expected nil response headers when response is nil, got %v", headers)
	}
}

type trackingReadCloser struct {
	io.Reader
	closed bool
}

func (r *trackingReadCloser) Close() error {
	r.closed = true
	return nil
}

type oneShotErrorReadCloser struct {
	data   []byte
	err    error
	read   bool
	closed bool
}

func (r *oneShotErrorReadCloser) Read(p []byte) (int, error) {
	if r.read {
		return 0, io.EOF
	}
	r.read = true
	n := copy(p, r.data)
	return n, r.err
}

func (r *oneShotErrorReadCloser) Close() error {
	r.closed = true
	return nil
}
