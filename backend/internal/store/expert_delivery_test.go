package store

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"energytool/backend/internal/domain"
	"energytool/backend/internal/expert"
)

func TestSpecialistReplyIsAvailableInClosedHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	repo, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	session := domain.Session{ID: "closed-session", Status: "closed", CreatedAt: now, UpdatedAt: now, ClosedAt: &now,
		Messages:   []domain.Message{{ID: "question", Role: "user", Content: "Вопрос клиента", Sequence: 1}},
		LastResult: &domain.ConsultationResult{Status: "knowledge_gap", Answer: "Вопрос передан специалисту"}}
	if err := repo.Create(session); err != nil {
		t.Fatal(err)
	}
	r := expert.Request{ID: "expert-request", SessionID: session.ID, OriginMessageID: "question", Status: expert.StatusAnswered, Version: 1,
		Reply: &expert.Reply{ID: "reply-1", RequestID: "expert-request", Body: "Точный ответ специалиста: <условие>.", CreatedAt: now, Delivery: expert.DeliveryPending}}
	if err := repo.SaveExpertRequest(r, 0); err != nil {
		t.Fatal(err)
	}
	delivered, err := repo.DeliverExpertReply(r, r.Reply.ID, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if delivered.Reply.Delivery != expert.DeliveryDelivered || !delivered.Reply.ToClosedSession || delivered.Reply.DeliveryAttempts != 1 {
		t.Fatalf("delivery metadata: %+v", delivered.Reply)
	}
	for attempt := 0; attempt < 2; attempt++ {
		got, err := repo.Get(session.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != "closed" || got.ClosedAt == nil || !got.ClosedAt.Equal(now) {
			t.Fatal("delivery reopened the closed session")
		}
		if len(got.Messages) != 2 || got.Messages[1].Content != r.Reply.Body || got.Messages[1].Sequence != 2 {
			t.Fatalf("missing or duplicate specialist answer: %+v", got.Messages)
		}
		meta := got.Messages[1].Response
		if meta == nil || meta.Origin != "specialist" || meta.ReplyID != r.Reply.ID || meta.RequestID != r.ID || meta.Status != "expert_answer" {
			t.Fatalf("lost specialist attribution: %+v", meta)
		}
		if !reflect.DeepEqual(got.LastResult, session.LastResult) {
			t.Fatal("specialist answer replaced the model's result metadata")
		}
		replayed, err := repo.DeliverExpertReply(delivered, r.Reply.ID, session.ID)
		if err != nil || !reflect.DeepEqual(replayed, delivered) {
			t.Fatalf("delivery replay changed the record: %v", err)
		}
		repo, err = NewFileStore(path)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestLegacyClosedDeliveryBackfillPreservesRecordsAndIsIdempotent(t *testing.T) {
	for _, status := range []string{"closed", "active"} {
		t.Run(status, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sessions.json")
			repo, err := NewFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			session := domain.Session{ID: "legacy", Status: status, CreatedAt: now, UpdatedAt: now, Messages: []domain.Message{}}
			if status == "closed" {
				session.ClosedAt = &now
			}
			if err := repo.Create(session); err != nil {
				t.Fatal(err)
			}
			// This is the durable shape emitted by the old implementation: a
			// delivered reply to a closed session with no matching history item.
			r := expert.Request{ID: "legacy-request", SessionID: session.ID, OriginMessageID: "old-question", Status: expert.StatusAnswered, Version: 5,
				Reply: &expert.Reply{ID: "legacy-reply", RequestID: "legacy-request", Body: "Ответ, который раньше был недоступен", CreatedAt: now, DeliveredAt: &now, Delivery: expert.DeliveryDelivered, DeliveryAttempts: 1, ToClosedSession: true}}
			if err := repo.SaveExpertRequest(r, 0); err != nil {
				t.Fatal(err)
			}
			for restart := 0; restart < 3; restart++ {
				repo, err = NewFileStore(path)
				if err != nil {
					t.Fatal(err)
				}
				got, err := repo.Get(session.ID)
				if err != nil {
					t.Fatal(err)
				}
				if got.Status != status || !reflect.DeepEqual(got.ClosedAt, session.ClosedAt) || !got.UpdatedAt.Equal(now) {
					t.Fatalf("migration changed session state: %+v", got)
				}
				if len(got.Messages) != 1 || got.Messages[0].Content != r.Reply.Body || got.Messages[0].Response.Origin != "specialist" || got.Messages[0].Response.ReplyID != r.Reply.ID {
					t.Fatalf("migration missing attribution or duplicated reply: %+v", got.Messages)
				}
				stored, err := repo.GetExpertRequest(r.ID)
				if err != nil || !reflect.DeepEqual(stored, r) {
					t.Fatalf("migration modified original delivery record: %v\n%+v", err, stored)
				}
			}
		})
	}
}
