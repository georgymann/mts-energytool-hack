package expert

import (
	"errors"
	"sort"
	"sync"
)

var ErrNotFound = errors.New("not found")

// RequestStore is what the core API must provide. Save is a compare-and-set on the stored
// version: it fails with ErrVersionConflict if the stored version differs from previousVersion
// (0 = create). Request, reply, delivery intent, command ids and history are one record.
type RequestStore interface {
	Get(id string) (Request, error)
	GetByKey(sessionID, originMessageID string) (*Request, error) // nil, nil when absent
	Save(r Request, previousVersion int) error
	List(filter RequestFilter) ([]Request, error)
}

type RequestFilter struct {
	Status     RequestStatus // empty = any
	AssigneeID string        // empty = any
	SessionID  string        // empty = any
}

type KnowledgeStore interface {
	Get(id string) (KnowledgeEntry, error)
	Save(e KnowledgeEntry, previousVersion int) error
	List() ([]KnowledgeEntry, error)
}

// RegisterGapAtomically is the reference creation path: the key lookup and the insert happen
// under one lock, so a replayed or concurrent signal yields exactly one request.
func RegisterGapAtomically(store *MemoryStore, signal GapSignal, messages []ClientMessage, newID func() string) (Request, bool, error) {
	store.keyMu.Lock()
	defer store.keyMu.Unlock()
	existing, err := store.GetByKey(signal.SessionID, signal.OriginMessageID)
	if err != nil {
		return Request{}, false, err
	}
	r, created, err := RegisterGap(existing, signal, messages, newID(), signal.At)
	if err != nil || !created {
		return r, created, err
	}
	return r, true, store.Save(r, 0)
}

// MemoryStore is a reference implementation for tests and for the contract; the web prototype
// uses the team's persistent store with the same guarantees.
type MemoryStore struct {
	mu        sync.Mutex
	keyMu     sync.Mutex
	requests  map[string]Request
	keys      map[string]string
	knowledge map[string]KnowledgeEntry
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{requests: map[string]Request{}, keys: map[string]string{}, knowledge: map[string]KnowledgeEntry{}}
}

func (s *MemoryStore) Get(id string) (Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.requests[id]
	if !ok {
		return Request{}, ErrNotFound
	}
	return cloneRequest(r), nil
}

func (s *MemoryStore) GetByKey(sessionID, originMessageID string) (*Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.keys[RequestKey(sessionID, originMessageID)]
	if !ok {
		return nil, nil
	}
	r := cloneRequest(s.requests[id])
	return &r, nil
}

func (s *MemoryStore) Save(r Request, previousVersion int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.requests[r.ID]
	switch {
	case previousVersion == 0 && ok:
		return ErrVersionConflict
	case previousVersion != 0 && (!ok || stored.Version != previousVersion):
		return ErrVersionConflict
	}
	key := RequestKey(r.SessionID, r.OriginMessageID)
	if id, taken := s.keys[key]; taken && id != r.ID {
		return ErrVersionConflict
	}
	s.requests[r.ID], s.keys[key] = cloneRequest(r), r.ID
	return nil
}

func (s *MemoryStore) List(f RequestFilter) ([]Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Request{}
	for _, r := range s.requests {
		if (f.Status == "" || r.Status == f.Status) && (f.AssigneeID == "" || r.AssigneeID == f.AssigneeID) &&
			(f.SessionID == "" || r.SessionID == f.SessionID) {
			out = append(out, cloneRequest(r))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// KnowledgeMemory adapts MemoryStore to KnowledgeStore.
type KnowledgeMemory struct{ *MemoryStore }

func (k KnowledgeMemory) Get(id string) (KnowledgeEntry, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	e, ok := k.knowledge[id]
	if !ok {
		return KnowledgeEntry{}, ErrNotFound
	}
	return cloneEntry(e), nil
}

func (k KnowledgeMemory) Save(e KnowledgeEntry, previousVersion int) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	stored, ok := k.knowledge[e.ID]
	if (previousVersion == 0 && ok) || (previousVersion != 0 && (!ok || stored.Version != previousVersion)) {
		return ErrVersionConflict
	}
	k.knowledge[e.ID] = cloneEntry(e)
	return nil
}

func (k KnowledgeMemory) List() ([]KnowledgeEntry, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := []KnowledgeEntry{}
	for _, e := range k.knowledge {
		out = append(out, cloneEntry(e))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
