package billing

import (
	"context"
	"sync"
)

// DefaultQuota is the baseline plan every tenant starts on. It is returned by
// Get when a tenant has no explicitly-recorded quota yet (e.g. it has never
// been upgraded). This models "every tenant begins on the basic plan."
var DefaultQuota = Quota{
	MaxUsers:     10,
	MaxStorageGB: 5,
	MaxSeats:     10,
}

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
		return DefaultQuota, nil
	}
	return q, nil
}
