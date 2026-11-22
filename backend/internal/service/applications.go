package service

import (
	"energytool/backend/internal/business"
	"errors"
	"time"
)

type applicationStore interface {
	ListApplications(string) ([]business.ListItem, error)
	GetApplication(string) (business.ApplicationRecord, error)
	PilotMetrics(time.Time, time.Time, bool) (business.PilotMetrics, error)
	ChangeApplication(string, func(business.ApplicationRecord) (business.ApplicationRecord, []business.Event, error)) (business.ApplicationRecord, error)
}

func (s *Service) applicationStore() (applicationStore, error) {
	v, ok := s.store.(applicationStore)
	if !ok {
		return nil, errors.New("application store unavailable")
	}
	return v, nil
}
func (s *Service) ListApplications(status string) ([]business.ListItem, error) {
	db, e := s.applicationStore()
	if e != nil {
		return nil, e
	}
	return db.ListApplications(status)
}
func (s *Service) GetApplication(id string) (business.ApplicationRecord, error) {
	db, e := s.applicationStore()
	if e != nil {
		return business.ApplicationRecord{}, e
	}
	return db.GetApplication(id)
}
func (s *Service) PilotMetrics(from, to time.Time, demo bool) (business.PilotMetrics, error) {
	db, e := s.applicationStore()
	if e != nil {
		return business.PilotMetrics{}, e
	}
	return db.PilotMetrics(from, to, demo)
}
func (s *Service) ChangeApplication(id string, mutate func(business.ApplicationRecord) (business.ApplicationRecord, []business.Event, error)) (business.ApplicationRecord, error) {
	db, e := s.applicationStore()
	if e != nil {
		return business.ApplicationRecord{}, e
	}
	r, e := db.GetApplication(id)
	if e != nil {
		return r, e
	}
	unlock := s.lock(r.SessionID)
	defer unlock()
	return db.ChangeApplication(id, mutate)
}
