package harpoon

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCallBufferOrdersNewestFirst(t *testing.T) {
	buffer := NewCallBuffer()

	for i := 0; i < 12; i++ {
		buffer.RecordCall(CallEntry{
			Timestamp: time.Unix(int64(i), 0).UTC(),
			Label:     "svc",
			Method:    "GET",
			URL:       "https://example.com",
			Status:    200,
		})
	}

	snapshot := buffer.Snapshot(10, "")
	require.Len(t, snapshot, 10)
	require.Equal(t, time.Unix(11, 0).UTC(), snapshot[0].Timestamp)
	require.Equal(t, time.Unix(2, 0).UTC(), snapshot[9].Timestamp)
}

func TestCallBufferFiltersByLabel(t *testing.T) {
	buffer := NewCallBuffer()
	buffer.RecordCall(CallEntry{Label: "auth", Method: "GET", Timestamp: time.Unix(1, 0).UTC()})
	buffer.RecordCall(CallEntry{Label: "billing", Method: "GET", Timestamp: time.Unix(2, 0).UTC()})
	buffer.RecordCall(CallEntry{Label: "auth", Method: "POST", Timestamp: time.Unix(3, 0).UTC()})

	snapshot := buffer.Snapshot(10, "auth")
	require.Len(t, snapshot, 2)
	require.Equal(t, "auth", snapshot[0].Label)
	require.Equal(t, time.Unix(3, 0).UTC(), snapshot[0].Timestamp)
	require.Equal(t, time.Unix(1, 0).UTC(), snapshot[1].Timestamp)
}
