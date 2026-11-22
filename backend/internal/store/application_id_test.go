package store

import (
	"encoding/json"
	"energytool/backend/internal/domain"
	"os"
	"testing"
)

func TestLegacyApplicationIDPersistsWithoutChangingContent(t *testing.T) {
	path := t.TempDir() + "/sessions.json"
	initial := map[string]domain.Session{
		"old":      {ID: "old", Application: &domain.Application{Summary: "Исходная задача"}},
		"existing": {ID: "existing", Application: &domain.Application{ID: "existing-number", Summary: "Другая задача"}},
	}
	raw, _ := json.Marshal(initial)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		s, err := NewFileStore(path)
		if err != nil {
			t.Fatal(err)
		}
		old, err := s.Get("old")
		if err != nil {
			t.Fatal(err)
		}
		if old.Application.ID != "ET-old" || old.Application.Summary != "Исходная задача" {
			t.Fatal("migration changed content or lost ID")
		}
		existing, _ := s.Get("existing")
		if existing.Application.ID != "existing-number" {
			t.Fatal("existing ID overwritten")
		}
	}
}
