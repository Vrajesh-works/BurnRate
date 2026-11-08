// Package limiter implements per-tenant token-bucket rate limiting.
//
// Each (tenant, key) pair owns a bucket that holds up to Policy.Capacity
// tokens and refills continuously at Policy.RefillPerSecond. A check spends
// Cost tokens if they are available. The package has no I/O and takes an
// injectable clock so the math is testable without sleeping.
package limiter

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

// Clock returns the current time. Tests pass a fake.
type Clock func() time.Time

// Policy describes a bucket shape for one tenant.
type Policy struct {
	Capacity        float64 `json:"capacity"`
	RefillPerSecond float64 `json:"refill_per_second"`
}

// Validate reports whether the policy can be enforced.
func (p Policy) Validate() error {
	if !(p.Capacity > 0) || math.IsInf(p.Capacity, 0) {
		return errors.New("capacity must be a positive finite number")
	}
	if !(p.RefillPerSecond > 0) || math.IsInf(p.RefillPerSecond, 0) {
		return errors.New("refill_per_second must be a positive finite number")
	}
	return nil
}

// Request is one rate-limit check.
type Request struct {
	Tenant string
	Key    string
	Cost   float64
}

// Decision is the outcome of a check.
type Decision struct {
	Allowed    bool          `json:"allowed"`
	Remaining  float64       `json:"remaining"`
	Limit      float64       `json:"limit"`
	RetryAfter time.Duration `json:"-"`
}

// ErrInvalidRequest is returned for malformed checks.
var ErrInvalidRequest = errors.New("invalid request")

// Limiter makes token-bucket decisions.
type Limiter struct {
	store    Store
	policies *PolicyStore
	now      Clock
}

// New builds a Limiter. A nil clock means time.Now.
func New(store Store, policies *PolicyStore, now Clock) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{store: store, policies: policies, now: now}
}

// Check spends req.Cost tokens from the bucket for (tenant, key) if possible.
func (l *Limiter) Check(req Request) (Decision, error) {
	if req.Tenant == "" || req.Key == "" {
		return Decision{}, fmt.Errorf("%w: tenant and key are required", ErrInvalidRequest)
	}
	if req.Cost == 0 {
		req.Cost = 1
	}
	if req.Cost < 0 || math.IsNaN(req.Cost) || math.IsInf(req.Cost, 0) {
		return Decision{}, fmt.Errorf("%w: cost must be a positive finite number", ErrInvalidRequest)
	}

	pol := l.policies.Get(req.Tenant)
	if req.Cost > pol.Capacity {
		return Decision{}, fmt.Errorf("%w: cost %.0f exceeds bucket capacity %.0f", ErrInvalidRequest, req.Cost, pol.Capacity)
	}

	now := l.now()
	var dec Decision
	l.store.Update(bucketID(req.Tenant, req.Key), now, func(b *Bucket, fresh bool) {
		if fresh {
			b.Tokens = pol.Capacity
		} else {
			elapsed := now.Sub(b.UpdatedAt).Seconds()
			if elapsed > 0 {
				b.Tokens = math.Min(pol.Capacity, b.Tokens+elapsed*pol.RefillPerSecond)
			}
			// A policy shrink takes effect immediately.
			b.Tokens = math.Min(b.Tokens, pol.Capacity)
		}
		b.UpdatedAt = now

		dec.Limit = pol.Capacity
		if b.Tokens >= req.Cost {
			b.Tokens -= req.Cost
			dec.Allowed = true
		} else {
			missing := req.Cost - b.Tokens
			dec.RetryAfter = time.Duration(math.Ceil(missing / pol.RefillPerSecond * float64(time.Second)))
		}
		dec.Remaining = math.Floor(b.Tokens)
	})
	return dec, nil
}

// Policies exposes the policy store.
func (l *Limiter) Policies() *PolicyStore { return l.policies }

// ActiveBuckets reports how many buckets are held.
func (l *Limiter) ActiveBuckets() int { return l.store.Len() }

func bucketID(tenant, key string) string {
	// Length-prefix the tenant so ("a:b","c") and ("a","b:c") never collide.
	return fmt.Sprintf("%d:%s:%s", len(tenant), tenant, key)
}

// RunJanitor evicts buckets idle for longer than idle every interval until
// ctx is done. Run it in its own goroutine.
func (l *Limiter) RunJanitor(ctx context.Context, interval, idle time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.store.Evict(l.now().Add(-idle))
		}
	}
}
