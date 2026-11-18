// Package store implements a process-local repository for value records.
package store

import (
	"sort"
	"sync"
)

type Store[T any] struct {
	mu   sync.RWMutex
	data map[string]T
}

func New[T any]() *Store[T] {
	return &Store[T]{data: make(map[string]T)}
}

func (s *Store[T]) Get(id string) (T, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.data[id]
	return v, ok
}

// Insert returns the existing record on conflict. Check and insert are atomic.
func (s *Store[T]) Insert(id string, v T) (T, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, exists := s.data[id]; exists {
		return old, false
	}
	s.data[id] = v
	return v, true
}

// Update applies fn under the lock. fn must not perform I/O or reenter the store.
func (s *Store[T]) Update(id string, fn func(T) T) (T, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[id]
	if ok {
		v = fn(v)
		s.data[id] = v
	}
	return v, ok
}

func (s *Store[T]) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data[id]
	delete(s.data, id)
	return ok
}

func (s *Store[T]) List() []T {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]string, 0, len(s.data))
	for k := range s.data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	items := make([]T, 0, len(keys))
	for _, k := range keys {
		items = append(items, s.data[k])
	}
	return items
}
