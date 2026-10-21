package mapper

import (
	"context"
	"errors"
	"testing"
	"time"
)

// P3-8: a cancelled cleanup must not keep sleeping through the busy backoff and
// must surface the cancellation instead of a stale SQLITE_BUSY.
func TestRetryOnBusyContextStopsOnCancellationDuringBackoff(t *testing.T) {
	old := writeBusyRetryDelays
	writeBusyRetryDelays = []time.Duration{time.Second}
	t.Cleanup(func() { writeBusyRetryDelays = old })

	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	start := time.Now()
	err := retryOnBusyContext(ctx, func() error {
		calls++
		cancel()
		return busyCodeError{5}
	})
	elapsed := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (no retry after cancellation)", calls)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("elapsed = %v, want the 1s backoff to be interrupted by the cancelled context", elapsed)
	}
}

func TestRetryOnBusyContextStillRetriesWhenNotCancelled(t *testing.T) {
	withFastWriteRetry(t, 2)
	calls := 0
	err := retryOnBusyContext(context.Background(), func() error {
		calls++
		if calls < 3 {
			return busyCodeError{5}
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("err = %v, calls = %d; want success on the third attempt", err, calls)
	}
}

func TestRetryOnBusyContextNilContextFallsBack(t *testing.T) {
	withFastWriteRetry(t, 1)
	calls := 0
	if err := retryOnBusyContext(nil, func() error { //nolint:staticcheck // nil context is explicitly supported
		calls++
		if calls == 1 {
			return busyCodeError{5}
		}
		return nil
	}); err != nil {
		t.Fatalf("retryOnBusyContext(nil) error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}
