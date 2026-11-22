package store

import (
	"path/filepath"
	"testing"
	"time"

	"energytool/backend/internal/domain"
)

func TestFileStorePersistsAndReturnsIndependentCopies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "sessions.json")
	first, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	session := domain.Session{ID: "s1", Status: "active", CreatedAt: time.Now().UTC(), Messages: []domain.Message{{ID: "m1", Role: "user", Content: "hello", Sequence: 1}}, Lead: domain.LeadState{SessionID: "s1", Fields: map[string]string{"company": "Example"}}}
	if err := first.Create(session); err != nil {
		t.Fatal(err)
	}
	loaded, err := first.Get("s1")
	if err != nil {
		t.Fatal(err)
	}
	loaded.Messages[0].Content = "mutated"
	loaded.Lead.Fields["company"] = "mutated"
	again, err := first.Get("s1")
	if err != nil {
		t.Fatal(err)
	}
	if again.Messages[0].Content != "hello" || again.Lead.Fields["company"] != "Example" {
		t.Fatal("store returned mutable internal state")
	}
	second, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := second.Get("s1")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Messages[0].Content != "hello" {
		t.Fatalf("persisted message = %q", reloaded.Messages[0].Content)
	}
}
