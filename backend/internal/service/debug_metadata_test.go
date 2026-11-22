package service

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"energytool/backend/internal/domain"
	"energytool/backend/internal/store"
)

func TestFailedMLCallKeepsMeasuredTimeAndSafeDebug(t *testing.T) {
	ml := &fakeML{fn: func(req domain.ConsultationRequest) (domain.ConsultationResult, error) {
		time.Sleep(25 * time.Millisecond)
		return domain.ConsultationResult{}, errors.New("private upstream address and key must not appear")
	}}
	app := newTestService(t, ml)
	session, _ := app.CreateSession()
	failed, err := app.SendMessage(context.Background(), session.ID, "Вопрос")
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != "technical_error" || failed.ElapsedMS < 20 {
		t.Fatalf("failure lost actual elapsed time: %#v", failed)
	}
	stored, _ := app.GetSession(session.ID)
	meta := stored.Messages[1].Response
	if meta == nil || meta.ElapsedMS != failed.ElapsedMS || meta.Debug["error_code"] != "consultation_unavailable" {
		t.Fatalf("failed answer metadata was not persisted: %#v", meta)
	}
	if _, exists := meta.Debug["ml_elapsed_ms"]; exists {
		t.Fatal("transport failure invented ML processing time")
	}
	raw, _ := json.Marshal(stored)
	if strings.Contains(string(raw), "private upstream") {
		t.Fatal("raw upstream error leaked into history")
	}
}

func TestEachAnswerDebugSurvivesNextTurnAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	repo, err := store.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	call := 0
	ml := &fakeML{fn: func(req domain.ConsultationRequest) (domain.ConsultationResult, error) {
		call++
		answer := result(req.SessionID, "Ответ", domain.LeadState{SessionID: req.SessionID})
		id := "kb/first.md#one"
		if call == 2 {
			id = "kb/second.md#two"
		}
		answer.Debug = map[string]interface{}{"retrieved_sources": []string{id}, "repairs": call - 1}
		return answer, nil
	}}
	app := New(repo, ml)
	session, _ := app.CreateSession()
	for _, text := range []string{"Первый вопрос", "Следующий вопрос"} {
		if _, err := app.SendMessage(context.Background(), session.ID, text); err != nil {
			t.Fatal(err)
		}
	}
	restarted, err := store.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := restarted.Get(session.ID)
	for i, want := range []string{"kb/first.md#one", "kb/second.md#two"} {
		meta := got.Messages[2*i+1].Response
		if meta == nil || meta.Debug == nil {
			t.Fatal("response debug missing after restart")
		}
		ids, ok := meta.Debug["retrieved_sources"].([]interface{})
		if !ok || len(ids) != 1 || ids[0] != want {
			t.Fatalf("answer %d has wrong context: %#v", i, meta.Debug)
		}
		if meta.Debug["ml_elapsed_ms"] != float64(10) || meta.Debug["backend_elapsed_ms"] != float64(meta.ElapsedMS) {
			t.Fatalf("timing origins were not retained: %#v", meta)
		}
	}
}
