package limiter

import (
	"errors"
	"sync"
)

// PolicyStore holds the default policy and per-tenant overrides.
type PolicyStore struct {
	mu        sync.RWMutex
	def       Policy
	overrides map[string]Policy
}

// NewPolicyStore returns a store using def for tenants without an override.
func NewPolicyStore(def Policy) (*PolicyStore, error) {
	if err := def.Validate(); err != nil {
		return nil, err
	}
	return &PolicyStore{def: def, overrides: make(map[string]Policy)}, nil
}

// Get returns the effective policy for tenant.
func (s *PolicyStore) Get(tenant string) Policy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p, ok := s.overrides[tenant]; ok {
		return p
	}
	return s.def
}

// Lookup returns the override for tenant and whether one exists.
func (s *PolicyStore) Lookup(tenant string) (Policy, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.overrides[tenant]
	return p, ok
}

// Set stores an override for tenant.
func (s *PolicyStore) Set(tenant string, p Policy) error {
	if tenant == "" {
		return errors.New("tenant is required")
	}
	if err := p.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.overrides[tenant] = p
	return nil
}

// Default returns the default policy.
func (s *PolicyStore) Default() Policy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.def
}
