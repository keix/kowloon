// Package memory is the v0 in-process implementation of idempotency.Store.
// State is lost on restart — that is the trade-off for zero infrastructure;
// a DynamoDB implementation lands later for cross-restart persistence.
package memory

import (
	"context"
	"sync"

	"github.com/keix/kowloon"
	"github.com/keix/kowloon/internal/idempotency"
)

type Store struct {
	mu      sync.RWMutex
	entries map[string]kowloon.IndexResultResponse
}

func New() *Store {
	return &Store{entries: make(map[string]kowloon.IndexResultResponse)}
}

func (s *Store) Lookup(_ context.Context, key idempotency.Key) (kowloon.IndexResultResponse, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	resp, ok := s.entries[key.String()]
	return resp, ok, nil
}

func (s *Store) Save(_ context.Context, key idempotency.Key, resp kowloon.IndexResultResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[key.String()] = resp
	return nil
}

// Len reports the current entry count. Useful for tests and for the
// admin endpoint that will eventually expose idempotency stats.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}
