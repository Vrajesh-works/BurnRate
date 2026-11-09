package limiter

import (
	"hash/fnv"
	"sync"
	"time"
)

// Bucket is the mutable state of one token bucket.
type Bucket struct {
	Tokens    float64
	UpdatedAt time.Time
}

// Store holds buckets. Update must run fn atomically for the given id.
// fresh is true when the bucket did not exist before the call.
//
// The in-memory implementation below is the only one today; a Redis adapter
// can satisfy the same interface later (see docs/PLAN.md, Future work).
type Store interface {
	Update(id string, now time.Time, fn func(b *Bucket, fresh bool))
	Evict(idleSince time.Time) int
	Len() int
}

const shardCount = 32

// MemoryStore is a sharded in-memory Store.
type MemoryStore struct {
	shards [shardCount]shard
}

type shard struct {
	mu      sync.Mutex
	buckets map[string]*Bucket
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	s := &MemoryStore{}
	for i := range s.shards {
		s.shards[i].buckets = make(map[string]*Bucket)
	}
	return s
}

func (s *MemoryStore) shardFor(id string) *shard {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return &s.shards[h.Sum32()%shardCount]
}

// Update implements Store.
func (s *MemoryStore) Update(id string, now time.Time, fn func(b *Bucket, fresh bool)) {
	sh := s.shardFor(id)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	b, ok := sh.buckets[id]
	if !ok {
		b = &Bucket{UpdatedAt: now}
		sh.buckets[id] = b
	}
	fn(b, !ok)
}

// Evict removes buckets not touched since idleSince and returns the count.
// An idle bucket has refilled anyway, so dropping it does not change decisions
// as long as idleSince is at least capacity/refill seconds in the past.
func (s *MemoryStore) Evict(idleSince time.Time) int {
	n := 0
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.Lock()
		for id, b := range sh.buckets {
			if b.UpdatedAt.Before(idleSince) {
				delete(sh.buckets, id)
				n++
			}
		}
		sh.mu.Unlock()
	}
	return n
}

// Len implements Store.
func (s *MemoryStore) Len() int {
	n := 0
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.Lock()
		n += len(sh.buckets)
		sh.mu.Unlock()
	}
	return n
}
