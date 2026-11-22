package store

import (
	"encoding/json"
	"energytool/backend/internal/business"
	"energytool/backend/internal/domain"
	"sort"
	"time"
)

func copyMap[K comparable, V any](input map[K]V) map[K]V {
	out := make(map[K]V, len(input))
	for k, v := range input {
		out[k] = v
	}
	return out
}
func cloneRecord(r business.ApplicationRecord) business.ApplicationRecord {
	b, _ := json.Marshal(r)
	var out business.ApplicationRecord
	_ = json.Unmarshal(b, &out)
	return out
}
func (s *FileStore) addEvents(events ...business.Event) {
	for _, e := range events {
		if e.ID != "" {
			if _, ok := s.events[e.ID]; !ok {
				s.events[e.ID] = e
			}
		}
	}
}

// Called under the write lock. Backfills old sessions and commits business facts
// in the same atomic file replacement as the user's application.
func (s *FileStore) syncBusiness(session domain.Session) error {
	s.addEvents(business.SessionStarted(session.ID, session.Demo, session.CreatedAt))
	for _, m := range session.Messages {
		if m.Role == "user" {
			s.addEvents(business.ConsultationRequested(session.ID, m.ID, session.Demo, m.CreatedAt))
		}
		if m.Role == "assistant" && m.Response != nil && m.Response.Status != "technical_error" {
			s.addEvents(business.FirstAnswerReceived(session.ID, session.Demo, m.CreatedAt))
		}
	}
	if session.Application == nil {
		return nil
	}
	var existing *business.ApplicationRecord
	if old, ok := s.applications[session.Application.ID]; ok {
		existing = &old
	}
	at := session.Application.SavedAt
	if at.IsZero() {
		at = session.UpdatedAt
	}
	rec, events, err := business.SaveApplication(existing, session, session.Application.ID, session.Demo, at)
	if err != nil {
		return err
	}
	s.applications[rec.ID] = rec
	s.addEvents(events...)
	return nil
}
func (s *FileStore) ListApplications(status string) ([]business.ListItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []business.ListItem{}
	for _, r := range s.applications {
		if status == "" || string(r.Status) == status {
			out = append(out, business.NewListItem(r))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}
func (s *FileStore) GetApplication(id string) (business.ApplicationRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.applications[id]
	if !ok {
		return r, ErrNotFound
	}
	return cloneRecord(r), nil
}
func (s *FileStore) PilotMetrics(from, to time.Time, includeDemo bool) (business.PilotMetrics, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	events := make([]business.Event, 0, len(s.events))
	for _, e := range s.events {
		events = append(events, e)
	}
	return business.ComputePilotMetrics(events, from, to, includeDemo), nil
}
func (s *FileStore) ChangeApplication(id string, mutate func(business.ApplicationRecord) (business.ApplicationRecord, []business.Event, error)) (business.ApplicationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.applications[id]
	if !ok {
		return old, ErrNotFound
	}
	next, events, err := mutate(cloneRecord(old))
	if err != nil {
		return old, err
	}
	previous := s.data[old.SessionID]
	session, err := clone(previous)
	if err != nil {
		return old, err
	}
	session.Application = &next.Form
	s.applications[id] = next
	s.data[session.ID] = session
	oldEvents := copyMap(s.events)
	s.addEvents(events...)
	if err := s.persist(); err != nil {
		s.applications[id] = old
		s.data[session.ID] = previous
		s.events = oldEvents
		return old, err
	}
	return cloneRecord(next), nil
}
