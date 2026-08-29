package billing

import (
	"context"
	"fmt"
	"sync"
)

type Quota struct {
	MaxUsers     int
	MaxStorageGB int
	MaxSeats     int
}

type QuotaStore interface {
	Set(ctx context.Context, tenantID string, q Quota) error
	Get(ctx context.Context, tenantID string) (Quota, error)
}

type InMemoryQuotaStore struct {
	mu   sync.Mutex
	data map[string]Quota
}

func NewInMemoryQuotaStore() *InMemoryQuotaStore {
	return &InMemoryQuotaStore{data: make(map[string]Quota)}
}

func (s *InMemoryQuotaStore) Set(_ context.Context, tenantID string, q Quota) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[tenantID] = q
	return nil
}

func (s *InMemoryQuotaStore) Get(_ context.Context, tenantID string) (Quota, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.data[tenantID]
	if !ok {
		return Quota{}, fmt.Errorf("quota not found for tenant %s", tenantID)
	}
	return q, nil
}
