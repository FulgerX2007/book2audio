package workpool

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunProcessesEveryItem(t *testing.T) {
	items := make([]int, 100)
	for i := range items {
		items[i] = i
	}
	var mu sync.Mutex
	var seen []int

	err := Run(context.Background(), 8, items, func(_ context.Context, item int) error {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, item)
		return nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	slices.Sort(seen)
	if !slices.Equal(seen, items) {
		t.Errorf("processed %d items, want every one of %d exactly once", len(seen), len(items))
	}
}

func TestRunLimitsConcurrency(t *testing.T) {
	for _, tt := range []struct{ n, want int }{{1, 1}, {3, 3}, {0, 1}, {-5, 1}} {
		var running, peak atomic.Int32
		err := Run(context.Background(), tt.n, make([]struct{}, 30), func(context.Context, struct{}) error {
			now := running.Add(1)
			for {
				old := peak.Load()
				if now <= old || peak.CompareAndSwap(old, now) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			running.Add(-1)
			return nil
		})
		if err != nil {
			t.Fatalf("n=%d: Run: %v", tt.n, err)
		}
		if got := int(peak.Load()); got != tt.want {
			t.Errorf("n=%d: peak concurrency = %d, want %d", tt.n, got, tt.want)
		}
	}
}

func TestRunEmpty(t *testing.T) {
	err := Run(context.Background(), 4, nil, func(context.Context, int) error {
		t.Error("fn was called without items")
		return nil
	})
	if err != nil {
		t.Errorf("Run: %v", err)
	}
}

func TestRunFatalErrorStopsTheRest(t *testing.T) {
	boom := errors.New("манифест не записан")
	var started atomic.Int32
	var sawCancel atomic.Bool

	err := Run(context.Background(), 2, make([]int, 200), func(ctx context.Context, _ int) error {
		if started.Add(1) == 1 {
			return boom
		}
		// The other running call sees the cancellation.
		select {
		case <-ctx.Done():
			sawCancel.Store(true)
		case <-time.After(2 * time.Second):
		}
		return nil
	})
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want %v", err, boom)
	}
	if n := started.Load(); n > 4 {
		t.Errorf("%d items were started after a fatal error, want the rest to be skipped", n)
	}
	if started.Load() > 1 && !sawCancel.Load() {
		t.Error("the running call did not see the context cancelled")
	}
}

func TestRunReturnsFirstError(t *testing.T) {
	first, second := errors.New("first"), errors.New("second")
	release := make(chan struct{})

	err := Run(context.Background(), 2, []int{0, 1}, func(_ context.Context, item int) error {
		if item == 0 {
			defer close(release)
			return first
		}
		<-release
		return second
	})
	if !errors.Is(err, first) {
		t.Errorf("error = %v, want the first one", err)
	}
}

func TestRunCancelledContext(t *testing.T) {
	t.Run("before the start", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var calls atomic.Int32
		err := Run(ctx, 2, make([]int, 10), func(context.Context, int) error {
			calls.Add(1)
			return nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
		if calls.Load() != 0 {
			t.Errorf("fn ran %d times with a cancelled context", calls.Load())
		}
	})
	t.Run("while running", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		var calls atomic.Int32
		err := Run(ctx, 1, make([]int, 50), func(ctx context.Context, _ int) error {
			if calls.Add(1) == 3 {
				cancel()
			}
			return nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
		if n := calls.Load(); n > 4 {
			t.Errorf("fn ran %d times, want it to stop soon after the cancellation", n)
		}
	})
}
