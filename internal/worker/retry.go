package worker

import (
	"context"
	"time"
)

type Attempt struct {
	Num int
}

type Retryer struct {
	MaxAttempts int
	BaseDelay   time.Duration
}

func (r Retryer) Do(ctx context.Context, fn func(ctx context.Context, a Attempt) error) error {
	max := r.MaxAttempts
	if max <= 0 {
		max = 3
	}
	delay := r.BaseDelay
	if delay <= 0 {
		delay = 800 * time.Millisecond
	}

	var last error
	for i := 1; i <= max; i++ {
		if err := fn(ctx, Attempt{Num: i}); err != nil {
			last = err
			if i == max {
				break
			}
			t := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-t.C:
			}
			// simple backoff
			delay = delay + delay/2
			continue
		}
		return nil
	}
	return last
}

