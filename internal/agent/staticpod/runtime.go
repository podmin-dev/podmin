// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package staticpod

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"time"
)

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
	delay := interval
	for {
		attempt, cancel := context.WithTimeout(ctx, timeout)
		err := r.Reconcile(attempt)
		cancel()
		if err != nil && ctx.Err() == nil {
			logger.Error("reconciliation failed", "error", err)
			if delay < time.Minute/2 {
				delay *= 2
			} else {
				delay = time.Minute
			}
		} else {
			delay = interval
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
