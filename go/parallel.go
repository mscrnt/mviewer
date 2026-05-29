package mview

import (
	"context"
	"sync"
)

// runParallel fans `work` out across at most `concurrency`
// goroutines, calling work(i) for i = 0..n-1. Returns the first
// error any work function returned, with the rest abandoned via
// ctx-derived cancellation.
//
// concurrency <= 0 collapses to sequential execution — useful for
// repeatable benchmarks.
//
// Why not errgroup? The package would otherwise pull in
// golang.org/x/sync just for one type, and the same shape ships in
// ~40 lines of stdlib code below.
func runParallel(ctx context.Context, n, concurrency int, work func(i int) error) error {
	if n <= 0 {
		return ctx.Err()
	}
	if concurrency <= 1 || n == 1 {
		for i := 0; i < n; i++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := work(i); err != nil {
				return err
			}
		}
		return nil
	}

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan int)
	errCh := make(chan error, concurrency)
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for g := 0; g < concurrency; g++ {
		go func() {
			defer wg.Done()
			for i := range jobs {
				if err := subCtx.Err(); err != nil {
					return
				}
				if err := work(i); err != nil {
					select {
					case errCh <- err:
					default:
					}
					cancel()
					return
				}
			}
		}()
	}
	for i := 0; i < n; i++ {
		select {
		case <-subCtx.Done():
			close(jobs)
			wg.Wait()
			if err := firstErr(errCh); err != nil {
				return err
			}
			return ctx.Err()
		case jobs <- i:
		}
	}
	close(jobs)
	wg.Wait()
	if err := firstErr(errCh); err != nil {
		return err
	}
	return ctx.Err()
}

func firstErr(ch chan error) error {
	select {
	case err := <-ch:
		return err
	default:
		return nil
	}
}
