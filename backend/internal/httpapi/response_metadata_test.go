package httpapi

import (
	"context"
	"energytool/backend/internal/service"
	"energytool/backend/internal/store"
	"testing"
)

func TestResponseMetadataSurvivesNextAnswerAndRestart(t *testing.T) {
	path := t.TempDir() + "/sessions.json"
	repo, err := store.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	app := service.New(repo, testML{})
	session, err := app.CreateSession()
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"Первый вопрос", "Другой вопрос"} {
		if _, err := app.SendMessage(context.Background(), session.ID, q); err != nil {
			t.Fatal(err)
		}
	}
	restarted, err := store.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := restarted.Get(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range got.Messages {
		if m.Role == "user" && m.Response != nil {
			t.Fatal("user got response metadata")
		}
		if m.Role == "assistant" && (m.Response == nil || m.Response.Status != "clarification" || m.Response.Sources == nil) {
			t.Fatalf("missing metadata for answer %d", i)
		}
	}
}
