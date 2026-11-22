package store

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"energytool/backend/internal/business"
	"energytool/backend/internal/domain"
	"energytool/backend/internal/expert"
)

var ErrNotFound = errors.New("session not found")

// Store persists the prototype state atomically in a private JSON file.
// The interface is intentionally small so a PostgreSQL implementation can replace it.
type Store interface {
	Create(domain.Session) error
	Get(string) (domain.Session, error)
	Save(domain.Session) error
	List() ([]domain.Session, error)
}

type ExpertStore interface {
	GetExpertRequest(string) (expert.Request, error)
	GetExpertRequestByKey(string, string) (*expert.Request, error)
	SaveExpertRequest(expert.Request, int) error
	ListExpertRequests(expert.RequestFilter) ([]expert.Request, error)
	GetKnowledge(string) (expert.KnowledgeEntry, error)
	SaveKnowledge(expert.KnowledgeEntry, int) error
	ListKnowledge() ([]expert.KnowledgeEntry, error)
	DeliverExpertReply(expert.Request, string, string) (expert.Request, error)
}

type FileStore struct {
	applications   map[string]business.ApplicationRecord
	events         map[string]business.Event
	mu             sync.RWMutex
	path           string
	data           map[string]domain.Session
	expertRequests map[string]expert.Request
	knowledge      map[string]expert.KnowledgeEntry
}

func NewFileStore(path string) (*FileStore, error) {
	s := &FileStore{applications: map[string]business.ApplicationRecord{}, events: map[string]business.Event{}, path: path, data: make(map[string]domain.Session), expertRequests: make(map[string]expert.Request), knowledge: make(map[string]expert.KnowledgeEntry)}
	if path == "" {
		return nil, errors.New("store path is required")
	}
	contents, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(contents) > 0 {
		var envelope struct {
			Sessions       map[string]domain.Session             `json:"sessions"`
			ExpertRequests map[string]expert.Request             `json:"expert_requests"`
			Knowledge      map[string]expert.KnowledgeEntry      `json:"knowledge_entries"`
			Applications   map[string]business.ApplicationRecord `json:"applications"`
			Events         map[string]business.Event             `json:"events"`
		}
		if err := json.Unmarshal(contents, &envelope); err != nil {
			return nil, errors.New("decode session store")
		}
		if envelope.Sessions == nil {
			// Backward compatibility with the original sessions-only JSON format.
			if err := json.Unmarshal(contents, &s.data); err != nil {
				return nil, errors.New("decode session store")
			}
		} else {
			s.data, s.expertRequests, s.knowledge = envelope.Sessions, envelope.ExpertRequests, envelope.Knowledge
			if envelope.Applications != nil {
				s.applications = envelope.Applications
			}
			if envelope.Events != nil {
				s.events = envelope.Events
			}
		}
		if s.data == nil {
			s.data = make(map[string]domain.Session)
		}
		if s.expertRequests == nil {
			s.expertRequests = make(map[string]expert.Request)
		}
		if s.knowledge == nil {
			s.knowledge = make(map[string]expert.KnowledgeEntry)
		}
	}
	changed := len(contents) > 0
	// Older versions marked replies to closed sessions as delivered without
	// adding them to the history. Restore only those durable replies, preserving
	// the original delivery metadata and the session's closed/active status.
	legacyReplies := []expert.Request{}
	for _, request := range s.expertRequests {
		if request.Reply != nil && request.Reply.Delivery == expert.DeliveryDelivered && request.Reply.ToClosedSession {
			legacyReplies = append(legacyReplies, request)
		}
	}
	sort.Slice(legacyReplies, func(i, j int) bool {
		if legacyReplies[i].Reply.CreatedAt.Equal(legacyReplies[j].Reply.CreatedAt) {
			return legacyReplies[i].ID < legacyReplies[j].ID
		}
		return legacyReplies[i].Reply.CreatedAt.Before(legacyReplies[j].Reply.CreatedAt)
	})
	for _, request := range legacyReplies {
		session, exists := s.data[request.SessionID]
		if !exists {
			continue
		}
		at := request.Reply.CreatedAt
		if request.Reply.DeliveredAt != nil {
			at = *request.Reply.DeliveredAt
		}
		if appendExpertReply(&session, request, at) {
			s.data[session.ID] = session
			changed = true
		}
	}
	for id, session := range s.data {
		if session.Application != nil && session.Application.ID == "" {
			session.Application.ID = domain.ApplicationID(session.ID)
			s.data[id] = session
			changed = true
		}
		if session.Application != nil && session.Application.Number == "" {
			number, err := s.nextApplicationNumber()
			if err != nil {
				return nil, err
			}
			session.Application.Number = number
			s.data[id] = session
			changed = true
		}
	}
	for _, session := range s.data {
		if err := s.syncBusiness(session); err != nil {
			return nil, err
		}
	}
	// Display-format migration: keep the full saved form and audit history intact.
	for id, record := range s.applications {
		if record.Card.Task.Origin == business.OriginForm && record.Card.Task.Value == record.Form.Summary {
			compact := business.FormTask(record.Form.Summary)
			if compact != record.Card.Task.Value {
				record.Card.Task.Value = compact
				s.applications[id] = record
				changed = true
			}
		}
	}
	if changed {
		if err := s.persist(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *FileStore) Create(session domain.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data[session.ID]; exists {
		return errors.New("session already exists")
	}
	oldApps, oldEvents := copyMap(s.applications), copyMap(s.events)
	if err := s.syncBusiness(session); err != nil {
		return err
	}
	s.data[session.ID] = session
	if err := s.persist(); err != nil {
		s.applications, s.events = oldApps, oldEvents
		delete(s.data, session.ID)
		return err
	}
	return nil
}

func (s *FileStore) Get(id string) (domain.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.data[id]
	if !ok {
		return domain.Session{}, ErrNotFound
	}
	return clone(session)
}

func (s *FileStore) Save(session domain.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, ok := s.data[session.ID]
	if !ok {
		return ErrNotFound
	}
	if session.Application != nil {
		if previous.Application != nil && previous.Application.Number != "" {
			session.Application.Number = previous.Application.Number
		} else {
			number, err := s.nextApplicationNumber()
			if err != nil {
				return err
			}
			session.Application.Number = number
		}
	}
	oldApps, oldEvents := copyMap(s.applications), copyMap(s.events)
	if err := s.syncBusiness(session); err != nil {
		s.applications, s.events = oldApps, oldEvents
		return err
	}
	s.data[session.ID] = session
	if err := s.persist(); err != nil {
		s.applications, s.events = oldApps, oldEvents
		s.data[session.ID] = previous
		return err
	}
	return nil
}

// Called only under the store write lock (or during construction). Numbers are
// allocated and saved in the same critical section, including concurrent saves.
func (s *FileStore) nextApplicationNumber() (string, error) {
	used := make(map[string]bool)
	for _, session := range s.data {
		if session.Application != nil {
			used[session.Application.Number] = true
		}
	}
	start, err := rand.Int(rand.Reader, big.NewInt(900000))
	if err != nil {
		return "", err
	}
	for offset := int64(0); offset < 900000; offset++ {
		number := fmt.Sprintf("%06d", 100000+(start.Int64()+offset)%900000)
		if !used[number] {
			return number, nil
		}
	}
	return "", errors.New("application number space exhausted")
}

func (s *FileStore) List() ([]domain.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]domain.Session, 0, len(s.data))
	for _, session := range s.data {
		copy, err := clone(session)
		if err != nil {
			return nil, err
		}
		result = append(result, copy)
	}
	return result, nil
}

func (s *FileStore) persist() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(struct {
		Sessions       map[string]domain.Session             `json:"sessions"`
		ExpertRequests map[string]expert.Request             `json:"expert_requests"`
		Knowledge      map[string]expert.KnowledgeEntry      `json:"knowledge_entries"`
		Applications   map[string]business.ApplicationRecord `json:"applications"`
		Events         map[string]business.Event             `json:"events"`
	}{s.data, s.expertRequests, s.knowledge, s.applications, s.events})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".sessions-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}

func (s *FileStore) GetExpertRequest(id string) (expert.Request, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.expertRequests[id]
	if !ok {
		return expert.Request{}, ErrNotFound
	}
	return r, nil
}
func (s *FileStore) GetExpertRequestByKey(sessionID, originID string) (*expert.Request, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.expertRequests {
		if r.SessionID == sessionID && r.OriginMessageID == originID {
			c := r
			return &c, nil
		}
	}
	return nil, nil
}
func (s *FileStore) SaveExpertRequest(r expert.Request, previous int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.expertRequests[r.ID]
	if previous == 0 && ok || previous != 0 && (!ok || old.Version != previous) {
		return expert.ErrVersionConflict
	}
	for _, x := range s.expertRequests {
		if x.ID != r.ID && x.SessionID == r.SessionID && x.OriginMessageID == r.OriginMessageID {
			return expert.ErrVersionConflict
		}
	}
	s.expertRequests[r.ID] = r
	return s.persist()
}
func (s *FileStore) ListExpertRequests(f expert.RequestFilter) ([]expert.Request, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []expert.Request{}
	for _, r := range s.expertRequests {
		if (f.Status == "" || r.Status == f.Status) && (f.AssigneeID == "" || r.AssigneeID == f.AssigneeID) && (f.SessionID == "" || r.SessionID == f.SessionID) {
			out = append(out, r)
		}
	}
	return out, nil
}
func (s *FileStore) GetKnowledge(id string) (expert.KnowledgeEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.knowledge[id]
	if !ok {
		return expert.KnowledgeEntry{}, ErrNotFound
	}
	return e, nil
}
func (s *FileStore) SaveKnowledge(e expert.KnowledgeEntry, previous int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.knowledge[e.ID]
	if previous == 0 && ok || previous != 0 && (!ok || old.Version != previous) {
		return expert.ErrVersionConflict
	}
	s.knowledge[e.ID] = e
	return s.persist()
}
func (s *FileStore) ListKnowledge() ([]expert.KnowledgeEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]expert.KnowledgeEntry, 0, len(s.knowledge))
	for _, e := range s.knowledge {
		out = append(out, e)
	}
	return out, nil
}

// DeliverExpertReply atomically appends the specialist answer to the original
// session and records delivery in the same durable JSON commit. Replays of the
// same reply ID are no-ops, including after a process restart. Closed sessions
// remain closed, but their saved history includes the answer as the contract requires.
func (s *FileStore) DeliverExpertReply(r expert.Request, replyID, sessionID string) (expert.Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.expertRequests[r.ID]
	if !ok || stored.Version != r.Version {
		return expert.Request{}, expert.ErrVersionConflict
	}
	session, ok := s.data[sessionID]
	if !ok {
		return expert.Request{}, ErrNotFound
	}
	previousSession, err := clone(session)
	if err != nil {
		return expert.Request{}, err
	}
	session = previousSession
	if stored.Reply == nil || stored.Reply.ID != replyID {
		return expert.Request{}, errors.New("expert reply is missing")
	}
	if stored.Reply.Delivery == expert.DeliveryDelivered {
		return stored, nil
	}
	appendExpertReply(&session, stored, time.Now().UTC())
	updated, err := expert.MarkDelivered(stored, expert.Actor{ID: "web-backend", Role: expert.RoleSystem}, expert.Command{ExpectedVersion: stored.Version, ID: "deliver-" + replyID}, replyID, session.Status != "active", time.Now())
	if err != nil {
		return stored, err
	}
	s.data[sessionID] = session
	s.expertRequests[updated.ID] = updated
	if err := s.persist(); err != nil {
		s.data[sessionID] = previousSession
		s.expertRequests[stored.ID] = stored
		return stored, err
	}
	return updated, nil
}

func appendExpertReply(session *domain.Session, request expert.Request, updatedAt time.Time) bool {
	reply := request.Reply
	if reply == nil || reply.ID == "" || request.SessionID != session.ID {
		return false
	}
	for _, message := range session.Messages {
		if message.Response != nil && message.Response.Origin == "specialist" && message.Response.ReplyID == reply.ID {
			return false
		}
	}
	session.Messages = append(session.Messages, domain.Message{ID: "expert-" + reply.ID, Role: "assistant", Content: reply.Body, Sequence: len(session.Messages) + 1, CreatedAt: reply.CreatedAt, Response: &domain.ResponseMeta{Origin: "specialist", ReplyID: reply.ID, RequestID: request.ID, Status: "expert_answer"}})
	if updatedAt.After(session.UpdatedAt) {
		session.UpdatedAt = updatedAt
	}
	return true
}

func clone(session domain.Session) (domain.Session, error) {
	data, err := json.Marshal(session)
	if err != nil {
		return domain.Session{}, err
	}
	var result domain.Session
	err = json.Unmarshal(data, &result)
	return result, err
}
