package delivery

import "sync"

// EndpointLimiter limits outstanding work per endpoint ID in ONE process.
// It never waits for capacity. Entries are removed when the last holder leaves.
// Share one pointer across all handlers in the Worker; it is not a global quota.
type EndpointLimiter struct {
	mu     sync.Mutex
	limit  int
	active map[uint64]int
}

func NewEndpointLimiter(limit int) *EndpointLimiter {
	if limit < 1 {
		panic("endpoint concurrency must be positive")
	}
	return &EndpointLimiter{limit: limit, active: make(map[uint64]int)}
}

func (l *EndpointLimiter) TryAcquire(id uint64) (release func(), ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active[id] >= l.limit {
		return nil, false
	}
	l.active[id]++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.active[id]--
			if l.active[id] == 0 {
				delete(l.active, id)
			}
		})
	}, true
}
