package admin

import (
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"sync"
)

// idSeq hands out catalog numbers from a file that only grows.
// Delete does not return a number: campaigns, clicks and offers keep the old one.
type idSeq struct {
	mu   sync.Mutex
	path string
}

// NewIDSeq returns a counter stored at path. An empty path disables numbering.
func NewIDSeq(path string) *idSeq {
	if path == "" {
		return nil
	}
	return &idSeq{path: path}
}

type seqFile struct {
	Next int `json:"next"`
}

// take returns the next number, at least floor, and stores the following one.
// The caller holds s.mu. floor is the first free number from existing ids.
func (s *idSeq) take(floor int) (int, error) {
	if s == nil || s.path == "" {
		return 0, errors.New("seq unavailable")
	}
	if floor < 1 {
		floor = 1
	}
	next, err := s.read()
	if err != nil {
		return 0, err
	}
	if next < floor {
		next = floor
	}
	if err := s.write(next + 1); err != nil {
		return 0, err
	}
	return next, nil
}

// note raises the counter past an explicit numeric id so a later take does not reuse it.
// The caller holds s.mu. id is one catalog id. Non-numeric ids are ignored.
// It does not return an error. A read or write failure leaves the counter unchanged.
func (s *idSeq) note(id string) {
	if s == nil {
		return
	}
	n, err := strconv.Atoi(id)
	if err != nil || n < 1 {
		return
	}
	s.raise(n)
}

// noteAll raises the counter past the largest numeric id in the list.
// ids are catalog ids from one import. A nil counter does nothing.
// It locks s.mu. Non-numeric ids are ignored. It does not return an error.
// A read or write failure leaves the counter unchanged.
func (s *idSeq) noteAll(ids []string) {
	if s == nil || s.path == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	max := 0
	for _, id := range ids {
		n, err := strconv.Atoi(id)
		if err != nil || n < 1 {
			continue
		}
		if n > max {
			max = n
		}
	}
	s.raise(max)
}

// raise moves the stored counter past n.
// The caller holds s.mu. n is a numeric id already known to be positive.
// It does not return an error. A read or write failure leaves the counter unchanged.
func (s *idSeq) raise(n int) {
	if s == nil || n < 1 {
		return
	}
	next, err := s.read()
	if err != nil {
		return
	}
	if n+1 > next {
		_ = s.write(n + 1)
	}
}

func (s *idSeq) read() (int, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	var f seqFile
	if err := json.Unmarshal(b, &f); err != nil {
		return 0, err
	}
	if f.Next < 0 {
		return 0, nil
	}
	return f.Next, nil
}

func (s *idSeq) write(next int) error {
	b, err := json.Marshal(seqFile{Next: next})
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return writeFileAtomic(s.path, b, 0o644)
}

// numericFloor is one past the largest integer in ids, and at least 1.
func numericFloor(ids []string) int {
	max := 0
	for _, id := range ids {
		n, err := strconv.Atoi(id)
		if err != nil || n < 0 {
			continue
		}
		if n > max {
			max = n
		}
	}
	if max < 1 && len(ids) == 0 {
		return 1
	}
	return max + 1
}
