// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package staticpod

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"
)

const convergenceRetryInterval = 100 * time.Millisecond

// Run polls revisions until context cancellation.
func (r *Reconciler) Run(ctx context.Context, logger *slog.Logger) {
	interval := r.config.PollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	timeout := r.config.ReconcileTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	failureDelay := interval
	for {
		delay := interval
		var err error
		ready := r.config.Identity.Revision() != 0
		if ready {
			attempt, cancel := context.WithTimeout(ctx, timeout)
			err = r.Reconcile(attempt)
			cancel()
		}
		if !ready {
			delay = convergenceRetryInterval
		} else if errors.Is(err, errDesiredStateChanged) {
			logger.Debug("reconciliation superseded", "reason", err)
			delay = convergenceRetryInterval
		} else if err != nil && ctx.Err() == nil {
			logger.Error("reconciliation failed", "error", err)
			if failureDelay < time.Minute/2 {
				failureDelay *= 2
			} else {
				failureDelay = time.Minute
			}
			delay = failureDelay
		} else {
			failureDelay = interval
		}
		timer := time.NewTimer(jitter(delay))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// jitter varies a polling delay by up to ten percent in either direction.
func jitter(delay time.Duration) time.Duration {
	variation := delay / 10
	if variation == 0 {
		return delay
	}
	return delay - variation + time.Duration(rand.Int64N(int64(variation*2)+1))
}
