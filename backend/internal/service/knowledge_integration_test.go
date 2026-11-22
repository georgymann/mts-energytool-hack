package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"energytool/backend/internal/domain"
	"energytool/backend/internal/expert"
	"energytool/backend/internal/store"
)

func TestKnowledgeLifecycleWritesOnlyApprovedSnapshot(t *testing.T) {
	repo, err := store.NewFileStore(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	app := New(repo, nil)
	snapshot := filepath.Join(t.TempDir(), "expert-kb.json")
	app.SetSnapshotPath(snapshot)
	author := expert.Actor{ID: "spec-1", Role: expert.RoleSpecialist}
	reviewer := expert.Actor{ID: "review-1", Role: expert.RoleReviewer}
	e, err := app.CreateKnowledge(author, "", expert.DraftInput{Title: "Тест", GeneralizedQuestion: "Как работает решение?", Answer: "Тестовый ответ без частных данных.", Applicability: "Для тестовых объектов.", Basis: "Проверка специалистом."}, false)
	if err != nil {
		t.Fatal(err)
	}
	if e.Revisions[0].Publication != expert.PubDraft {
		t.Fatal("draft was not created")
	}
	checklist := expert.ReviewChecklist{FactConfirmed: true, ConditionsStated: true, NoContradiction: true, NoPrivateData: true, Reusable: true}
	e, err = app.ApproveKnowledge(e.ID, reviewer, expert.Command{ExpectedVersion: e.Version, ID: "approve-1"}, 1, checklist)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		KBVersion string              `json:"kb_version"`
		Documents []expert.KBDocument `json:"documents"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.KBVersion == "" || len(payload.Documents) != 1 || payload.Documents[0].EntryID != e.ID {
		t.Fatalf("snapshot=%+v", payload)
	}
	if _, err := app.RevokeKnowledge(e.ID, reviewer, expert.Command{ExpectedVersion: e.Version, ID: "revoke-1"}, "Устарело"); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Documents) != 0 {
		t.Fatalf("revoked entry remains in snapshot: %+v", payload.Documents)
	}
}

func TestKnowledgeGapIsNotCreatedForOtherStatuses(t *testing.T) {
	repo, err := store.NewFileStore(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	app := New(repo, &fakeML{fn: func(req domain.ConsultationRequest) (domain.ConsultationResult, error) {
		return domain.ConsultationResult{Answer: "Уточните", Status: "clarification", Lead: domain.LeadState{SessionID: req.SessionID}, SchemaVersion: "1.0"}, nil
	}})
	session, err := app.CreateSession()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.SendMessage(context.Background(), session.ID, "Уточните"); err != nil {
		t.Fatal(err)
	}
	requests, err := app.ListExpertRequests(expert.RequestFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 0 {
		t.Fatalf("unexpected requests: %+v", requests)
	}
}
