package service

import (
	"energytool/backend/internal/domain"
	"energytool/backend/internal/expert"
	"energytool/backend/internal/store"
	"testing"
)

func TestPendingExpertGapSurvivesNewerAnswer(t *testing.T) {
	db, err := store.NewFileStore(t.TempDir() + "/sessions.json")
	if err != nil {
		t.Fatal(err)
	}
	app := New(db, nil)
	app.SetDemoMode(true)
	session, err := app.CreateSession()
	if err != nil {
		t.Fatal(err)
	}
	session.Messages = []domain.Message{{ID: "first-question", Role: "user", Content: "Есть ли интеграция с неизвестной системой?", Sequence: 1}, {ID: "later-question", Role: "user", Content: "Продолжим про учёт", Sequence: 2}}
	session.PendingExpertGaps = map[string]bool{"first-question": true}
	session.LastResult = &domain.ConsultationResult{Status: "answered", Answer: "Новый ответ"}
	if err = db.Save(session); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = app.RetryPendingExpertGaps(); err != nil {
			t.Fatal(err)
		}
	}
	records, err := app.ListExpertRequests(expert.RequestFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].OriginMessageID != "first-question" || !records[0].Demo {
		t.Fatal(records)
	}
	restored, _ := db.Get(session.ID)
	if len(restored.PendingExpertGaps) != 0 {
		t.Fatal(restored.PendingExpertGaps)
	}
}
