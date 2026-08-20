package internal

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestForwardingRoundTripperInjectsAndCapturesHeaders(t *testing.T) {
	t.Helper()

	wantRequest := http.Header{"X-Test": {"forward-me"}}
	wantResponse := http.Header{"X-Resp": {"ok"}, "Another": {"value"}}

	rt := NewForwardingRoundTripper(
		roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			got := req.Header.Values("X-Test")
			if diff := cmp.Diff(wantRequest["X-Test"], got, cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
				t.Fatalf("request headers mismatch (-want +got):\n%s", diff)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     wantResponse.Clone(),
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		}),
	)

	ctx, carrier, err := ContextWithHeaders(context.Background(), wantRequest)
	if err != nil {
		t.Fatalf("ContextWithHeaders: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.com", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}

	req.Header.Set("X-Test", "should-be-overwritten")

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()

	status, got := carrier.ResponseStatusAndHeaders()
	if diff := cmp.Diff(wantResponse, got, cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
		t.Fatalf("response headers mismatch (-want +got):\n%s", diff)
	}

	if status != http.StatusOK {
		t.Fatalf("response status mismatch: got %d, want %d", status, http.StatusOK)
	}
}

func TestContextWithHeadersRejectsNilContext(t *testing.T) {
	t.Helper()

	//lint:ignore SA1012 This intentionally passes a nil context to cover the explicit nil-guard.
	_, _, err := ContextWithHeaders(nil, http.Header{"X": {"y"}})
	if err == nil {
		t.Fatal("expected error for nil context")
	}
}

func TestHeaderCarrierRequestHeadersAreCloned(t *testing.T) {
	t.Helper()

	orig := http.Header{"X-Test": {"a", "b"}}
	_, carrier, err := ContextWithHeaders(context.Background(), orig)
	if err != nil {
		t.Fatalf("ContextWithHeaders: %v", err)
	}

	orig.Set("X-Test", "mutated")

	got := carrier.RequestHeaders()
	if got.Get("X-Test") == "mutated" {
		t.Fatal("expected request headers to be cloned from input")
	}
}

func TestHeaderCarrierApplyRequestHeadersOverridesExisting(t *testing.T) {
	t.Helper()

	_, carrier, err := ContextWithHeaders(context.Background(), http.Header{"X-Test": {"a", "b"}})
	if err != nil {
		t.Fatalf("ContextWithHeaders: %v", err)
	}

	dst := http.Header{"X-Test": {"old"}, "Other": {"keep"}}
	carrier.ApplyRequestHeaders(dst)

	if got := dst.Values("X-Test"); cmp.Diff([]string{"a", "b"}, got, cmpopts.SortSlices(func(a, b string) bool { return a < b })) != "" {
		t.Fatalf("ApplyRequestHeaders did not override values for X-Test, got %v", got)
	}
	if dst.Get("Other") != "keep" {
		t.Fatalf("ApplyRequestHeaders unexpectedly modified other headers: %v", dst)
	}
}

func TestHeaderCarrierStoreAndReturnResponseHeadersAreCloned(t *testing.T) {
	t.Helper()

	_, carrier, err := ContextWithHeaders(context.Background(), nil)
	if err != nil {
		t.Fatalf("ContextWithHeaders: %v", err)
	}

	respHdr := http.Header{"X-Resp": {"ok"}}
	carrier.StoreResponse(201, respHdr)
	respHdr.Set("X-Resp", "mutated")

	code, got := carrier.ResponseStatusAndHeaders()
	if code != 201 {
		t.Fatalf("status code mismatch: got %d, want %d", code, 201)
	}
	if got.Get("X-Resp") == "mutated" {
		t.Fatal("expected stored response headers to be cloned")
	}

	// Ensure returned headers are also defensive copies.
	got.Set("X-Resp", "changed-again")
	_, got2 := carrier.ResponseStatusAndHeaders()
	if got2.Get("X-Resp") == "changed-again" {
		t.Fatal("expected ResponseStatusAndHeaders to return a defensive copy")
	}
}

func TestHeaderCarrierNilIsSafe(t *testing.T) {
	t.Helper()

	var carrier *HeaderCarrier
	carrier.ApplyRequestHeaders(http.Header{})
	carrier.StoreResponse(200, http.Header{"X": {"y"}})
	carrier.StoreTransportError(errors.New("transport"))
	if code, hdr := carrier.ResponseStatusAndHeaders(); code != 0 || hdr != nil {
		t.Fatalf("expected nil carrier to return zero values, got (%d, %v)", code, hdr)
	}
	if carrier.TransportError() != nil {
		t.Fatal("expected nil carrier TransportError to return nil")
	}
	if carrier.RequestHeaders() != nil {
		t.Fatal("expected nil carrier RequestHeaders to return nil")
	}
}

func TestHeaderCarrierStoreAndReturnTransportError(t *testing.T) {
	t.Parallel()

	_, carrier, err := ContextWithHeaders(context.Background(), nil)
	if err != nil {
		t.Fatalf("ContextWithHeaders: %v", err)
	}
	wantErr := errors.New("dial failed")
	carrier.StoreTransportError(wantErr)
	if got := carrier.TransportError(); !errors.Is(got, wantErr) {
		t.Fatalf("transport error = %v, want %v", got, wantErr)
	}
}

func TestCarrierFromContextNilIsSafe(t *testing.T) {
	t.Helper()

	//lint:ignore SA1012 This intentionally passes a nil context to ensure CarrierFromContext does not panic.
	if CarrierFromContext(nil) != nil {
		t.Fatal("expected nil carrier for nil context")
	}
}
