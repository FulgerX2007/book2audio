// Package workpool runs the units of a pipeline stage with bounded
// concurrency.
package workpool

import (
	"context"
	"sync"
)

// Run calls fn for every item, at most n calls at a time, and waits for all
// of them.
//
// An error returned by fn is fatal: the context passed to the running calls
// is cancelled, the remaining items are not started and Run returns the
// first such error. A failure of a single unit that must not stop the stage
// is therefore recorded by fn itself, which then returns nil.
//
// When ctx is cancelled Run stops starting items and returns the cause.
func Run[T any](ctx context.Context, n int, items []T, fn func(ctx context.Context, item T) error) error {
	if n < 1 {
		n = 1
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	sem := make(chan struct{}, n)
	var wg sync.WaitGroup
start:
	for _, item := range items {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break start
		}
		wg.Go(func() {
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			if err := fn(ctx, item); err != nil {
				cancel(err) // only the first cause is kept
			}
		})
	}
	wg.Wait()
	return context.Cause(ctx)
}
