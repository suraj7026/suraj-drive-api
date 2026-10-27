package joblease

import (
	"context"
	"time"
)

const Duration = 2 * time.Minute
const Interval = 30 * time.Second

// Timeout allows sustained processing at 5 MiB/s in addition to the baseline.
func Timeout(base time.Duration, size int64) time.Duration {
	const rate = int64(5 << 20)
	if size <= 0 {
		return base
	}
	return base + time.Duration((size+rate-1)/rate)*time.Second
}

// Run cancels work if its fenced lease cannot be renewed. Joining the heartbeat
// ensures a finished job never leaves a renewal goroutine behind.
func Run(ctx context.Context, interval time.Duration, renew func(context.Context) error, work func(context.Context) error) error {
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := make(chan struct{})
	heartbeatDone := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				heartbeatDone <- nil
				return
			case <-workCtx.Done():
				heartbeatDone <- nil
				return
			case <-ticker.C:
				renewCtx, endRenewal := context.WithTimeout(workCtx, 10*time.Second)
				err := renew(renewCtx)
				endRenewal()
				if err != nil {
					cancel()
					heartbeatDone <- err
					return
				}
			}
		}
	}()
	err := work(workCtx)
	close(stop)
	renewalErr := <-heartbeatDone
	if err != nil {
		if renewalErr != nil {
			return renewalErr
		}
		return err
	}
	return nil
}
