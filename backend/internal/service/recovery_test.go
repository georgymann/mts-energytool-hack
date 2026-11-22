package service

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"energytool/backend/internal/domain"
	"energytool/backend/internal/store"
)

type waitingML struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (m *waitingML) Respond(ctx context.Context, req domain.ConsultationRequest) (domain.ConsultationResult, error) {
	if m.calls.Add(1) == 1 {
		close(m.started)
	}
	select {
	case <-m.release:
		return result(req.SessionID, "Ответ", domain.LeadState{SessionID: req.SessionID}), nil
	case <-ctx.Done():
		return domain.ConsultationResult{}, ctx.Err()
	}
}

func waitFor[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(2 * time.Second):
		t.Fatal("operation blocked behind model generation")
		var zero T
		return zero
	}
}

func TestSlowMessageAllowsApplicationAndResumeWithoutLosingState(t *testing.T) {
	repo, err := store.NewFileStore(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	ml := &waitingML{started: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(ml.release) }) })
	app := New(repo, ml)
	session, err := app.CreateSession()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := app.SendMessageWithID(context.Background(), session.ID, "slow", "Нужен учёт")
		done <- err
	}()
	waitFor(t, ml.started)
	if _, err := app.FindRequest(session.ID, "slow"); !errors.Is(err, ErrRequestPending) {
		t.Fatalf("inflight request lookup=%v", err)
	}
	snapshot, err := app.GetSession(session.ID)
	if err != nil || len(snapshot.Messages) != 1 || !snapshot.Requests["slow"].Pending {
		t.Fatalf("live request was treated as interrupted: %+v, %v", snapshot, err)
	}
	mutated := make(chan error, 1)
	go func() {
		_, err := app.SaveApplicationWithID(session.ID, "handoff", domain.Application{Email: "operator@example.test", Summary: "Нужен учёт"})
		if err == nil {
			_, err = app.CloseSession(session.ID)
		}
		if err == nil {
			_, err = app.ResumeSession(session.ID)
		}
		mutated <- err
	}()
	if err := waitFor(t, mutated); err != nil {
		t.Fatal(err)
	}
	// A second HTTP request can expire while waiting for the first turn. It
	// must never append a new user message after its caller has gone away.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	queued := make(chan error, 1)
	go func() {
		_, err := app.SendMessageWithID(ctx, session.ID, "expired", "Другой вопрос")
		queued <- err
	}()
	if err := waitFor(t, queued); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued request error=%v", err)
	}
	release.Do(func() { close(ml.release) })
	if err := waitFor(t, done); err != nil {
		t.Fatal(err)
	}
	saved, err := app.GetSession(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Application == nil || saved.Application.Email != "operator@example.test" || saved.Status != "active" || saved.ClosedAt != nil {
		t.Fatalf("slow response overwrote concurrent changes: %+v", saved)
	}
	if len(saved.Messages) != 2 || saved.Messages[1].Response.RequestID != "slow" || ml.calls.Load() != 1 {
		t.Fatalf("unexpected history/calls: %+v, %d", saved.Messages, ml.calls.Load())
	}
	if _, err := app.SendMessageWithID(context.Background(), session.ID, "slow", "Нужен учёт"); err != nil {
		t.Fatal(err)
	}
	if ml.calls.Load() != 1 {
		t.Fatal("replay ran the model twice")
	}
}

func TestInterruptedRequestsRecoverAfterRestartAndCanContinue(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "identified", true: "legacy"}[legacy], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sessions.json")
			repo, err := store.NewFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			app := New(repo, nil)
			session, err := app.CreateSession()
			if err != nil {
				t.Fatal(err)
			}
			session.Messages = append(session.Messages, domain.Message{ID: "user-1", Role: "user", Content: "Нужен учёт", Sequence: 1, CreatedAt: time.Now()})
			if !legacy {
				session.Requests["interrupted"] = domain.RequestRecord{Fingerprint: requestFingerprint("message", "Нужен учёт"), Pending: true, MessageID: "user-1"}
			}
			if err := repo.Save(session); err != nil {
				t.Fatal(err)
			}
			restarted, err := store.NewFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			ml := &fakeML{fn: func(req domain.ConsultationRequest) (domain.ConsultationResult, error) {
				return result(req.SessionID, "Продолжаем", domain.LeadState{SessionID: req.SessionID}), nil
			}}
			app = New(restarted, ml)
			for i := 0; i < 2; i++ {
				recovered, err := app.GetSession(session.ID)
				if err != nil {
					t.Fatal(err)
				}
				if len(recovered.Messages) != 2 || recovered.LastResult == nil || recovered.LastResult.Status != "technical_error" || recovered.LastResult.Debug["error_code"] != "request_interrupted" {
					t.Fatalf("interrupted turn did not settle exactly once: %+v", recovered)
				}
			}
			if !legacy {
				replayed, err := app.SendMessageWithID(context.Background(), session.ID, "interrupted", "Нужен учёт")
				if err != nil || replayed.Status != "technical_error" || len(ml.calls) != 0 {
					t.Fatalf("recovery replay duplicated a turn: %+v, %v", replayed, err)
				}
			}
			if _, err := app.SendMessageWithID(context.Background(), session.ID, "new-turn", "Продолжим"); err != nil {
				t.Fatal(err)
			}
			if len(ml.calls) != 1 || len(ml.calls[0].Messages) != 3 {
				t.Fatal("continuation lost the earlier question or duplicated it")
			}
		})
	}
}

func TestCanceledGenerationSettlesAndNextMessageWorks(t *testing.T) {
	repo, err := store.NewFileStore(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	ml := &waitingML{started: make(chan struct{}), release: make(chan struct{})}
	app := New(repo, ml)
	session, err := app.CreateSession()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := app.SendMessageWithID(ctx, session.ID, "canceled", "Первый вопрос")
		done <- err
	}()
	waitFor(t, ml.started)
	cancel()
	if err := waitFor(t, done); err != nil {
		t.Fatal(err)
	}
	failure, err := app.FindRequest(session.ID, "canceled")
	if err != nil || failure.Status != "technical_error" {
		t.Fatalf("canceled turn remains unsettled: %+v, %v", failure, err)
	}
	close(ml.release)
	if _, err := app.SendMessageWithID(context.Background(), session.ID, "continue", "Продолжение"); err != nil {
		t.Fatal(err)
	}
	saved, err := app.GetSession(session.ID)
	if err != nil || len(saved.Messages) != 4 || saved.LastResult.Status != "answered" {
		t.Fatalf("continuation failed: %+v, %v", saved, err)
	}
}
