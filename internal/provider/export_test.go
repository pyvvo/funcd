package provider

import (
	"context"
	"sync"
)

// ScratchCalls records the scratch filesystems a runtime made and removed, in place of real images.
type ScratchCalls struct {
	mu            sync.Mutex
	Made, Removed []string // the image paths
	live          map[string]bool
}

func (s *ScratchCalls) Make(_ context.Context, image, _ string, _ int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Made = append(s.Made, image)
	if s.live == nil {
		s.live = map[string]bool{}
	}
	s.live[image] = true
	return nil
}

func (s *ScratchCalls) Remove(image, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Removed = append(s.Removed, image)
	delete(s.live, image)
	return nil
}

// Holds reports whether image was made and not removed since.
func (s *ScratchCalls) Holds(image string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live[image]
}

// UseScratch makes r record its scratch filesystems in calls instead of making them.
func UseScratch(r Runtime, calls *ScratchCalls) { r.(*engineRuntime).scratch.fs = calls }
