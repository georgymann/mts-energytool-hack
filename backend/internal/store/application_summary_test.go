package store

import (
	"energytool/backend/internal/domain"
	"testing"
	"time"
)

func TestStructuredSummaryMigrationKeepsSavedForm(t *testing.T) {
	path := t.TempDir() + "/sessions.json"
	db, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	full := "Задача клиента:\n• Автоматизировать учёт\n\nОбъект:\n• Склад\n\nИзвестные параметры:\n• Счётчики есть"
	now := time.Now().UTC()
	session := domain.Session{ID: "summary-session", Status: "active", CreatedAt: now, UpdatedAt: now, Application: &domain.Application{ID: "ET-summary", Number: "123456", Email: "test@example.test", Summary: full, SavedAt: now}}
	if err = db.Create(session); err != nil {
		t.Fatal(err)
	}
	record := db.applications["ET-summary"]
	if record.Card.Task.Value != "Автоматизировать учёт" {
		t.Fatal(record.Card.Task)
	}
	record.Card.Task.Value = full // Emulate the earlier persisted representation.
	db.applications[record.ID] = record
	if err = db.persist(); err != nil {
		t.Fatal(err)
	}
	restored, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := restored.GetApplication(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.Card.Task.Value != "Автоматизировать учёт" || migrated.Form.Summary != full || migrated.Version != record.Version {
		t.Fatal(migrated)
	}
}
