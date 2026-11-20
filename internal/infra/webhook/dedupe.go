package webhook

import "sync"

// DeliveryStore remembers which deliveries have been accepted. Claim returns
// true when id is new and false when the same id was claimed before. A false
// return means the caller must skip the event: GitHub retries deliveries, and
// processing one twice would publish the same verdict twice.
type DeliveryStore interface {
	Claim(id string) bool
}

// MemoryDeliveryStore is an in-memory DeliveryStore for the local prototype.
// A durable implementation can replace it without changing the receiver.
type MemoryDeliveryStore struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

// NewMemoryDeliveryStore builds an empty store.
func NewMemoryDeliveryStore() *MemoryDeliveryStore {
	return &MemoryDeliveryStore{seen: make(map[string]struct{})}
}

// Claim records id and reports whether this is its first delivery. The mutex
// makes concurrent deliveries with the same id converge on one winner.
func (s *MemoryDeliveryStore) Claim(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.seen[id]; ok {
		return false
	}
	s.seen[id] = struct{}{}
	return true
}
