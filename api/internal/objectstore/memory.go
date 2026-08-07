package objectstore

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
)

type Memory struct {
	mu      sync.RWMutex
	objects map[string][]byte
}

func NewMemory() *Memory {
	return &Memory{objects: make(map[string][]byte)}
}

func (m *Memory) Put(_ context.Context, key string, content []byte, _ string) error {
	if int64(len(content)) > maxObjectSize {
		return errors.New("object exceeds 32 MiB")
	}
	key, err := normalizeKey(key)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.objects[key] = append([]byte(nil), content...)
	m.mu.Unlock()
	return nil
}

func (m *Memory) Get(_ context.Context, key string) ([]byte, error) {
	key, err := normalizeKey(key)
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	content, found := m.objects[key]
	m.mu.RUnlock()
	if !found {
		return nil, ErrNotFound
	}
	return append([]byte(nil), content...), nil
}

func (m *Memory) List(_ context.Context, prefix string) ([]string, error) {
	prefix, err := normalizeKeyPrefix(prefix)
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	keys := make([]string, 0)
	for key := range m.objects {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	m.mu.RUnlock()
	sort.Strings(keys)
	return keys, nil
}

func (m *Memory) Delete(key string) {
	m.mu.Lock()
	delete(m.objects, key)
	m.mu.Unlock()
}
