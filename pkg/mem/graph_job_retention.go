package mem

import "time"

// These small terminal views explain a recently evicted job to polling clients.
// They are process-local, contain no request/result payload, and are themselves
// bounded. Reading an expired job never renews its lifetime.
const (
	MaxKnowledgeJobTombstones      = 128
	KnowledgeJobTombstoneRetention = time.Hour
)

type knowledgeJobTombstone[T any] struct {
	view     T
	forgetAt time.Time
}

type knowledgeJobTombstones[T any] struct {
	items map[string]knowledgeJobTombstone[T]
	order []string
}

func (s *knowledgeJobTombstones[T]) cleanup(now time.Time) {
	kept := s.order[:0]
	for _, id := range s.order {
		item, ok := s.items[id]
		if !ok || !now.Before(item.forgetAt) {
			delete(s.items, id)
			continue
		}
		kept = append(kept, id)
	}
	clear(s.order[len(kept):])
	s.order = kept
}

func (s *knowledgeJobTombstones[T]) remember(id string, view T, expiredAt, now time.Time) {
	s.cleanup(now)
	forgetAt := expiredAt.Add(KnowledgeJobTombstoneRetention)
	if !now.Before(forgetAt) {
		return
	}
	if s.items == nil {
		s.items = make(map[string]knowledgeJobTombstone[T])
	}
	if _, exists := s.items[id]; exists {
		return
	}
	for len(s.order) >= MaxKnowledgeJobTombstones {
		delete(s.items, s.order[0])
		copy(s.order, s.order[1:])
		s.order[len(s.order)-1] = ""
		s.order = s.order[:len(s.order)-1]
	}
	s.items[id] = knowledgeJobTombstone[T]{view: view, forgetAt: forgetAt}
	s.order = append(s.order, id)
}

func (s *knowledgeJobTombstones[T]) get(id string) (T, bool) {
	item, ok := s.items[id]
	return item.view, ok
}
