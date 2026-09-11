package internal

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/controlplane"
	"github.com/openai/tunnel-client/pkg/healthstate"
	"github.com/openai/tunnel-client/pkg/tunnelctx"
	"github.com/openai/tunnel-client/pkg/types"
)

func TestResponseDeliveryHealthObservesActualHTTPDisposition(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		codes       []int
		disposition string
		accepted    uint64
	}{
		{"accepted", []int{200}, "accepted", 1},
		{"unknown", []int{404}, "already_fulfilled_or_unknown", 0},
		{"recovered", []int{503, 200}, "accepted", 1},
		{"failed", []int{401}, "failed", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempt := 0
			server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				code := tc.codes[attempt]
				attempt++
				w.WriteHeader(code)
			}))
			client, err := NewTunnelServiceClient(t.Context(), &config.ControlPlaneConfig{BaseURL: mustParseURL(t, server.URL), TunnelID: "test", APIKey: "test"}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
			require.NoError(t, err)
			h := controlplane.NewDeliveryHealth()
			client.ObserveHealth(nil, h)
			client.retrySleep = func(context.Context, time.Duration) bool {
				snapshot := h.Snapshot(time.Now())
				require.Equal(t, healthstate.StatusDegraded, snapshot.Status)
				require.Equal(t, 503, snapshot.Details.(healthstate.DeliveryDetails).HTTPStatus)
				return true
			}
			ctx := tunnelctx.ContextWithShardToken(t.Context(), "test")
			_, err = client.PostResponse(ctx, "test", types.NewNotificationAck(types.DefaultChannel, 200, nil))
			if tc.disposition == "failed" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			d := h.Snapshot(time.Now()).Details.(healthstate.DeliveryDetails)
			require.Equal(t, tc.disposition, d.Disposition)
			require.Equal(t, tc.accepted, d.Accepted)
			require.Equal(t, uint64(len(tc.codes)), d.Attempts)
			require.Zero(t, d.InProgress)
		})
	}
}

type emptyThenBlockedHealthFetcher struct {
	calls  int
	second chan struct{}
}

func (f *emptyThenBlockedHealthFetcher) Poll(ctx context.Context, _ int) ([]controlplane.PolledCommand, types.TunnelServiceRequestID, error) {
	f.calls++
	if f.calls == 1 {
		return nil, "", nil
	}
	close(f.second)
	<-ctx.Done()
	return nil, "", ctx.Err()
}

func TestPollHealthRecordsSuccessfulEmptyPoll(t *testing.T) {
	t.Parallel()
	h := controlplane.NewPollHealth(nil)
	fetcher := &emptyThenBlockedHealthFetcher{second: make(chan struct{})}
	queue := &chanQueue{ch: make(chan controlplane.PolledCommand, 1)}
	poller, err := NewPoller(queue, fetcher, newDiscardLogger(), testMeterProvider.Meter("health-test"), time.Second, 0, 0, 0, h)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); poller.Run(ctx) }()
	select {
	case <-fetcher.second:
	case <-time.After(time.Second):
		t.Fatal("second poll did not start")
	}
	snapshot := h.Snapshot(time.Now())
	require.Equal(t, healthstate.StatusOK, snapshot.Status)
	require.NotNil(t, snapshot.Details.(healthstate.PollingDetails).LastSuccess)
	cancel()
	<-done
	require.Equal(t, "stopped", h.Snapshot(time.Now()).State)
	require.Zero(t, h.Snapshot(time.Now()).Details.(healthstate.PollingDetails).ConsecutiveFailures)
}
