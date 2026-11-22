package store

import (
	"testing"
	"time"

	"energytool/backend/internal/business"
	"energytool/backend/internal/domain"
)

func TestRestartPreservesExplicitManagerEmailContactTime(t *testing.T) {
	path := t.TempDir() + "/sessions.json"
	db, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	session := domain.Session{ID: "email-time", Status: "active", CreatedAt: now, UpdatedAt: now,
		Application: &domain.Application{ID: "ET-email-time", Email: "audit@example.test", Summary: "Учёт офиса", CallbackMode: "asap", SavedAt: now}}
	if err := db.Create(session); err != nil {
		t.Fatal(err)
	}
	explicit := business.Field{Value: "Ответить письмом во вторник после 16:00", Status: business.Confirmed, Origin: business.OriginManager}
	_, err = db.ChangeApplication(session.Application.ID, func(record business.ApplicationRecord) (business.ApplicationRecord, []business.Event, error) {
		record.Card.ContactTime = explicit
		return record, nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	record, err := restored.GetApplication(session.Application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Card.ContactTime.Value != explicit.Value || record.Card.ContactTime.Origin != explicit.Origin || record.Card.ContactTime.Status != explicit.Status {
		t.Fatalf("restart erased an explicit preference: %+v", record.Card.ContactTime)
	}
}
