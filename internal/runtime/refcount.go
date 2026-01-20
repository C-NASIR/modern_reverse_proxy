package runtime

import "time"

func (s *Snapshot) IncRef() {
	if s == nil {
		return
	}
	s.refCount.Add(1)
}

func (s *Snapshot) DecRef() {
	if s == nil {
		return
	}
	s.refCount.Add(-1)
}

func (s *Snapshot) RefCount() int64 {
	if s == nil {
		return 0
	}
	return s.refCount.Load()
}

func (s *Snapshot) MarkRetired(now time.Time) {
	if s == nil {
		return
	}
	s.retiredAt.Store(now.UnixNano())
}

func (s *Snapshot) Retired() bool {
	if s == nil {
		return false
	}
	return s.retiredAt.Load() > 0
}

func (s *Snapshot) RetiredAt() time.Time {
	if s == nil {
		return time.Time{}
	}
	retiredAt := s.retiredAt.Load()
	if retiredAt == 0 {
		return time.Time{}
	}
	return time.Unix(0, retiredAt)
}
