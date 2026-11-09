package limiter

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestLimiter(t *testing.T, def Policy) (*Limiter, *fakeClock) {
	t.Helper()
	ps, err := NewPolicyStore(def)
	if err != nil {
		t.Fatalf("NewPolicyStore: %v", err)
	}
	clk := newFakeClock()
	return New(NewMemoryStore(), ps, clk.Now), clk
}

func TestCheckSpendsUntilEmptyThenDenies(t *testing.T) {
	l, _ := newTestLimiter(t, Policy{Capacity: 3, RefillPerSecond: 1})
	for i := 0; i < 3; i++ {
		d, err := l.Check(Request{Tenant: "acme", Key: "auth"})
		if err != nil {
			t.Fatal(err)
		}
		if !d.Allowed {
			t.Fatalf("check %d denied, want allowed", i)
		}
		if want := float64(2 - i); d.Remaining != want {
			t.Fatalf("check %d remaining=%v want %v", i, d.Remaining, want)
		}
	}
	d, err := l.Check(Request{Tenant: "acme", Key: "auth"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Allowed {
		t.Fatal("4th check allowed, want denied")
	}
	if d.RetryAfter != time.Second {
		t.Fatalf("retry after=%v want 1s", d.RetryAfter)
	}
	if d.Limit != 3 {
		t.Fatalf("limit=%v want 3", d.Limit)
	}
}

func TestRefillOverTime(t *testing.T) {
	l, clk := newTestLimiter(t, Policy{Capacity: 10, RefillPerSecond: 2})
	if _, err := l.Check(Request{Tenant: "t", Key: "k", Cost: 10}); err != nil {
		t.Fatal(err)
	}
	clk.Advance(1500 * time.Millisecond) // +3 tokens
	d, _ := l.Check(Request{Tenant: "t", Key: "k", Cost: 3})
	if !d.Allowed || d.Remaining != 0 {
		t.Fatalf("got %+v, want allowed with 0 remaining", d)
	}
	clk.Advance(time.Hour) // refill caps at capacity
	d, _ = l.Check(Request{Tenant: "t", Key: "k", Cost: 1})
	if d.Remaining != 9 {
		t.Fatalf("remaining=%v want 9 (capped at capacity)", d.Remaining)
	}
}

func TestRetryAfterReflectsMissingTokens(t *testing.T) {
	l, _ := newTestLimiter(t, Policy{Capacity: 5, RefillPerSecond: 0.5})
	_, _ = l.Check(Request{Tenant: "t", Key: "k", Cost: 5})
	d, _ := l.Check(Request{Tenant: "t", Key: "k", Cost: 2})
	if d.Allowed {
		t.Fatal("want denied")
	}
	if d.RetryAfter != 4*time.Second {
		t.Fatalf("retry after=%v want 4s", d.RetryAfter)
	}
}

func TestKeysAndTenantsAreIsolated(t *testing.T) {
	l, _ := newTestLimiter(t, Policy{Capacity: 1, RefillPerSecond: 1})
	cases := []Request{
		{Tenant: "a", Key: "x"},
		{Tenant: "a", Key: "y"},
		{Tenant: "b", Key: "x"},
		{Tenant: "a:b", Key: "c"},
		{Tenant: "a", Key: "b:c"},
	}
	for _, r := range cases {
		d, err := l.Check(r)
		if err != nil || !d.Allowed {
			t.Fatalf("%+v: allowed=%v err=%v, want first check allowed", r, d.Allowed, err)
		}
	}
	if got := l.ActiveBuckets(); got != len(cases) {
		t.Fatalf("active buckets=%d want %d", got, len(cases))
	}
}

func TestTenantPolicyOverride(t *testing.T) {
	l, _ := newTestLimiter(t, Policy{Capacity: 1, RefillPerSecond: 1})
	if err := l.Policies().Set("big", Policy{Capacity: 100, RefillPerSecond: 50}); err != nil {
		t.Fatal(err)
	}
	d, _ := l.Check(Request{Tenant: "big", Key: "k", Cost: 60})
	if !d.Allowed || d.Limit != 100 || d.Remaining != 40 {
		t.Fatalf("got %+v", d)
	}
}

func TestPolicyShrinkClampsExistingBucket(t *testing.T) {
	l, _ := newTestLimiter(t, Policy{Capacity: 100, RefillPerSecond: 1})
	_, _ = l.Check(Request{Tenant: "t", Key: "k"}) // bucket holds 99
	if err := l.Policies().Set("t", Policy{Capacity: 5, RefillPerSecond: 1}); err != nil {
		t.Fatal(err)
	}
	d, _ := l.Check(Request{Tenant: "t", Key: "k"})
	if d.Remaining != 4 {
		t.Fatalf("remaining=%v want 4 after shrink", d.Remaining)
	}
}

func TestInvalidRequests(t *testing.T) {
	l, _ := newTestLimiter(t, Policy{Capacity: 10, RefillPerSecond: 1})
	for name, r := range map[string]Request{
		"no tenant":       {Key: "k"},
		"no key":          {Tenant: "t"},
		"negative cost":   {Tenant: "t", Key: "k", Cost: -1},
		"cost > capacity": {Tenant: "t", Key: "k", Cost: 11},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := l.Check(r); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("err=%v want ErrInvalidRequest", err)
			}
		})
	}
}

func TestPolicyValidation(t *testing.T) {
	for _, p := range []Policy{{0, 1}, {1, 0}, {-1, 1}, {1, -2}} {
		if err := p.Validate(); err == nil {
			t.Errorf("%+v validated, want error", p)
		}
	}
	if _, err := NewPolicyStore(Policy{}); err == nil {
		t.Error("NewPolicyStore accepted zero policy")
	}
	ps, _ := NewPolicyStore(Policy{1, 1})
	if err := ps.Set("", Policy{1, 1}); err == nil {
		t.Error("Set accepted empty tenant")
	}
	if _, ok := ps.Lookup("nobody"); ok {
		t.Error("Lookup found missing override")
	}
}

func TestEvictDropsIdleBuckets(t *testing.T) {
	l, clk := newTestLimiter(t, Policy{Capacity: 1, RefillPerSecond: 1})
	_, _ = l.Check(Request{Tenant: "t", Key: "old"})
	clk.Advance(10 * time.Minute)
	_, _ = l.Check(Request{Tenant: "t", Key: "new"})
	n := l.store.Evict(clk.Now().Add(-time.Minute))
	if n != 1 || l.ActiveBuckets() != 1 {
		t.Fatalf("evicted=%d active=%d, want 1 and 1", n, l.ActiveBuckets())
	}
}

func TestConcurrentChecksNeverOverspend(t *testing.T) {
	l, _ := newTestLimiter(t, Policy{Capacity: 1000, RefillPerSecond: 0.0001})
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				d, err := l.Check(Request{Tenant: "t", Key: "hot"})
				if err != nil {
					panic(err)
				}
				if d.Allowed {
					mu.Lock()
					allowed++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	if allowed != 1000 {
		t.Fatalf("allowed=%d want exactly 1000", allowed)
	}
}

func BenchmarkCheckParallel(b *testing.B) {
	ps, _ := NewPolicyStore(Policy{Capacity: 1e9, RefillPerSecond: 1e9})
	l := New(NewMemoryStore(), ps, nil)
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			_, _ = l.Check(Request{Tenant: "t", Key: fmt.Sprintf("k%d", i%1024)})
			i++
		}
	})
}

func TestRunJanitorStopsOnCancel(t *testing.T) {
	ps, _ := NewPolicyStore(Policy{Capacity: 1, RefillPerSecond: 1})
	l := New(NewMemoryStore(), ps, nil)
	_, _ = l.Check(Request{Tenant: "t", Key: "k"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.RunJanitor(ctx, time.Millisecond, 0); close(done) }()
	deadline := time.After(2 * time.Second)
	for l.ActiveBuckets() != 0 {
		select {
		case <-deadline:
			t.Fatal("janitor never evicted idle bucket")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	<-done
}
