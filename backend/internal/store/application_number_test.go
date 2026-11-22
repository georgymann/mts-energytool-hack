package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"

	"energytool/backend/internal/domain"
)

func TestApplicationNumbersUniqueStableAndPersistent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	s, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	for _, id := range ids {
		if err := s.Create(domain.Session{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if err := s.Save(domain.Session{ID: id, Application: &domain.Application{ID: domain.ApplicationID(id), Number: "111111", Summary: "Тест"}}); err != nil {
				t.Error(err)
			}
		}(id)
	}
	wg.Wait()
	seen := map[string]bool{}
	numbers := map[string]string{}
	for _, id := range ids {
		session, err := s.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		n := session.Application.Number
		if !regexp.MustCompile(`^[1-9][0-9]{5}$`).MatchString(n) || seen[n] {
			t.Fatalf("invalid or duplicate number %q", n)
		}
		seen[n] = true
		numbers[id] = n
		session.Application.Number = "999999"
		session.Application.Summary = "Изменено"
		if err := s.Save(session); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		session, _ := reopened.Get(id)
		if session.Application.Number != numbers[id] {
			t.Fatal("number changed after edit/restart")
		}
	}
}

func TestExistingApplicationsReceiveReadableNumbers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	data, _ := json.Marshal(map[string]domain.Session{"legacy": {ID: "legacy", Application: &domain.Application{ID: "ET-existing", Summary: "Сохранённая заявка"}}})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	session, _ := s.Get("legacy")
	if len(session.Application.Number) != 6 || session.Application.ID != "ET-existing" {
		t.Fatal("migration must preserve internal ID and add number")
	}
	reopened, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := reopened.Get("legacy")
	if again.Application.Number != session.Application.Number {
		t.Fatal("migration number not persisted")
	}
}
