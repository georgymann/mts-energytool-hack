package expert

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)

var (
	coordinator = Actor{ID: "lead-1", Role: RoleCoordinator}
	specialist  = Actor{ID: "spec-1", Role: RoleSpecialist}
	other       = Actor{ID: "spec-2", Role: RoleSpecialist}
	system      = Actor{ID: "web-backend", Role: RoleSystem}
)

func dialog() []ClientMessage {
	return []ClientMessage{
		{ID: "m1", Role: "user", Content: "У нас склад в Казани, 120 кВт.", Sequence: 1},
		{ID: "m2", Role: "assistant", Content: "Подойдёт солнечная станция.", Sequence: 2},
		{ID: "m3", Role: "user", Content: "Кровля из сэндвич-панелей.", Sequence: 3},
		{ID: "m4", Role: "assistant", Content: "Уточните площадь.", Sequence: 4},
		{ID: "m5", Role: "user", Content: "Около 2000 м².", Sequence: 5},
		{ID: "m6", Role: "assistant", Content: "Понял.", Sequence: 6},
		{ID: "m7", Role: "user", Content: "А нужно ли согласовывать станцию с МЧС?", Sequence: 7},
		{ID: "m8", Role: "assistant", Content: "В материалах нет сведений.", Sequence: 8},
		{ID: "m9", Role: "user", Content: "Ещё вопрос", Sequence: 9},
	}
}

func gap(session, origin string) GapSignal {
	return GapSignal{SessionID: session, OriginMessageID: origin, ResultStatus: "knowledge_gap", Demo: true, At: t0}
}

func register(t *testing.T) Request {
	t.Helper()
	r, created, err := RegisterGap(nil, gap("s1", "m7"), dialog(), "req-1", t0)
	if err != nil || !created {
		t.Fatalf("register: %v %v", created, err)
	}
	return r
}

func cmd(r Request, id string) Command { return Command{ExpectedVersion: r.Version, ID: id} }

func ok(t *testing.T) func(Request, error) Request {
	return func(r Request, err error) Request {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
}

func answered(t *testing.T) Request {
	t.Helper()
	r := register(t)
	r = ok(t)(Assign(r, coordinator, cmd(r, "c1"), "spec-1", t0))
	r = ok(t)(Take(r, specialist, cmd(r, "c2"), t0))
	return ok(t)(Answer(r, specialist, cmd(r, "c3"), "reply-1", "Для вашего склада в Казани согласование не требуется.", t0))
}

func TestOnlyKnowledgeGapCreatesRequest(t *testing.T) {
	for _, status := range []string{"answered", "clarification", "out_of_scope", "technical_error", ""} {
		s := gap("s1", "m7")
		s.ResultStatus = status
		if _, created, err := RegisterGap(nil, s, dialog(), "x", t0); created || err != nil {
			t.Errorf("%q must not create a request (created=%v err=%v)", status, created, err)
		}
	}
	if _, _, err := RegisterGap(nil, gap("s1", "m8"), dialog(), "x", t0); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("origin must be a client message, not an assistant one")
	}
	if _, _, err := RegisterGap(nil, gap("s1", "nope"), dialog(), "x", t0); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("origin must be a stored message")
	}
}

func TestRequestHasQuestionAndBoundedClientContext(t *testing.T) {
	r := register(t)
	if r.Question != "А нужно ли согласовывать станцию с МЧС?" || r.Status != StatusQueued || r.Version != 1 || !r.Demo {
		t.Fatalf("request: %+v", r)
	}
	if len(r.Context) != 3 || r.Context[0].MessageID != "m1" || r.Context[2].MessageID != "m5" {
		t.Fatalf("context = 3 previous client messages, oldest first: %+v", r.Context)
	}
	for _, c := range r.Context {
		if strings.Contains(c.Text, "солнечная станция") {
			t.Fatal("assistant text must not be in the context")
		}
	}
	// No phone, no application: still a valid request.
	if r.ApplicationID != "" || StatusForClient(r).Code != "awaiting_assignment" {
		t.Fatalf("no phone needed; truthful status: %+v", StatusForClient(r))
	}
}

func TestReplayedSignalReturnsSameRequestAndClientsAreIsolated(t *testing.T) {
	r := register(t)
	again, created, err := RegisterGap(&r, gap("s1", "m7"), dialog(), "req-other", t0.Add(time.Minute))
	if err != nil || created || again.ID != r.ID || again.Version != r.Version {
		t.Fatalf("replay must return the stored request: %v %v %+v", created, err, again)
	}
	if _, _, err := RegisterGap(&r, gap("s2", "m7"), dialog(), "x", t0); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("a request of another session must not be returned for a new key")
	}
	other := []ClientMessage{{ID: "k1", Role: "user", Content: "А нужно ли согласовывать станцию с МЧС?", Sequence: 1}}
	r2, created, err := RegisterGap(nil, gap("s2", "k1"), other, "req-2", t0)
	if err != nil || !created || r2.ID == r.ID || len(r2.Context) != 0 || r2.SessionID != "s2" {
		t.Fatalf("same question of another client is a separate request: %+v", r2)
	}
	if RequestKey("s1", "m7") == RequestKey("s2", "m7") {
		t.Fatal("key must include the session")
	}
}

func TestLifecycleRolesAndClientStatus(t *testing.T) {
	r := register(t)
	if _, err := Assign(r, specialist, cmd(r, "a0"), "spec-1", t0); !errors.Is(err, ErrForbidden) {
		t.Fatal("only a coordinator assigns")
	}
	if _, err := Take(r, specialist, cmd(r, "t0"), t0); !errors.Is(err, ErrForbidden) {
		t.Fatal("an unassigned specialist cannot take")
	}
	r = ok(t)(Assign(r, coordinator, cmd(r, "a1"), "spec-1", t0))
	if StatusForClient(r).Code != "assigned" {
		t.Fatal("«передано специалисту» only after a stored assignment")
	}
	if _, err := Take(r, other, cmd(r, "t1"), t0); !errors.Is(err, ErrForbidden) {
		t.Fatal("only the assignee takes the request")
	}
	if _, err := Answer(r, specialist, cmd(r, "x"), "rp", "текст", t0); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal("answer requires in_review")
	}
	r = ok(t)(Take(r, specialist, cmd(r, "t2"), t0))
	if StatusForClient(r).Code != "in_review" {
		t.Fatal("in review")
	}
	// Reassignment from in_review returns the request to the queue for the new specialist.
	moved := ok(t)(Assign(r, coordinator, cmd(r, "a2"), "spec-2", t0))
	if moved.Status != StatusQueued || moved.AssigneeID != "spec-2" {
		t.Fatalf("reassign: %+v", moved)
	}
	if _, err := Answer(moved, specialist, cmd(moved, "x2"), "rp", "текст", t0); !errors.Is(err, ErrForbidden) {
		t.Fatal("the previous assignee cannot answer after reassignment")
	}
	final := answered(t)
	if final.Status != StatusAnswered || final.Reply.Delivery != DeliveryPending || final.Reply.Label != "Ответ специалиста" ||
		StatusForClient(final).Code != "answer_pending" {
		t.Fatalf("answered is not delivered: %+v", final.Reply)
	}
	if len(final.History) != 4 || final.History[3].ActorID != "spec-1" || final.History[1].AssigneeID != "spec-1" {
		t.Fatalf("history with authors: %+v", final.History)
	}
}

func TestReplyIsStoredOnceAndConflictsAreExplicit(t *testing.T) {
	r := answered(t)
	body := "Для вашего склада в Казани согласование не требуется."
	same, err := Answer(r, specialist, Command{ExpectedVersion: r.Version - 1, ID: "c3"}, "reply-1", body, t0)
	if err != nil || same.Version != r.Version || same.Reply.ID != "reply-1" {
		t.Fatalf("replayed answer command must be a no-op: %v", err)
	}
	if _, err := Answer(r, specialist, Command{ExpectedVersion: r.Version - 1, ID: "c3"}, "reply-1", "другой текст", t0); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("same command id with another body must conflict, got %v", err)
	}
	if _, err := Answer(r, specialist, cmd(r, "c9"), "reply-2", body, t0); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal("a second reply must be refused")
	}
	if _, err := Cancel(r, coordinator, Command{ExpectedVersion: 1, ID: "z"}, "other", "", t0); !errors.Is(err, ErrVersionConflict) {
		t.Fatal("stale version must conflict")
	}
	if r.Reply.Body != body {
		t.Fatal("reply text must be stored exactly")
	}
}

func TestDeliveryIsSeparateAndRetriesTheSameReply(t *testing.T) {
	r := answered(t)
	if _, err := MarkDelivered(r, specialist, cmd(r, "d0"), "reply-1", false, t0); !errors.Is(err, ErrForbidden) {
		t.Fatal("delivery callbacks come from the system actor")
	}
	r = ok(t)(MarkDeliveryFailed(r, system, cmd(r, "d1"), "reply-1", "session_store_unavailable", t0))
	if r.Status != StatusAnswered || r.Reply.Delivery != DeliveryFailed || StatusForClient(r).Code != "answer_pending" {
		t.Fatalf("failed delivery keeps answered and is visible: %+v", r.Reply)
	}
	if _, err := MarkDelivered(r, system, cmd(r, "d2"), "reply-1", false, t0); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal("failed must be retried before delivery")
	}
	r = ok(t)(RetryDelivery(r, system, cmd(r, "d3"), "reply-1", t0))
	deliveryCommand := cmd(r, "d4")
	r = ok(t)(MarkDelivered(r, system, deliveryCommand, "reply-1", true, t0.Add(time.Minute)))
	if r.Reply.Delivery != DeliveryDelivered || r.Reply.DeliveryAttempts != 2 || !r.Reply.ToClosedSession ||
		r.Reply.ID != "reply-1" || StatusForClient(r).Code != "answered" {
		t.Fatalf("delivered same reply to a closed session's history: %+v", r.Reply)
	}
	again, err := MarkDelivered(r, system, deliveryCommand, "reply-1", true, t0)
	if err != nil || again.Version != r.Version {
		t.Fatal("repeated delivery mark is a no-op")
	}
}

func TestCancelNeedsReasonAndIsFinal(t *testing.T) {
	r := register(t)
	if _, err := Cancel(r, coordinator, cmd(r, "x1"), "bogus", "", t0); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("reason is required")
	}
	r = ok(t)(Cancel(r, coordinator, cmd(r, "x2"), "duplicate", "повтор вопроса", t0))
	if r.Status != StatusCancelled || StatusForClient(r).Code != "cancelled" {
		t.Fatal("cancelled")
	}
	if _, err := Assign(r, coordinator, cmd(r, "x3"), "spec-1", t0); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal("cancelled request cannot be reassigned")
	}
	if _, err := Cancel(answered(t), coordinator, Command{ExpectedVersion: 4, ID: "x4"}, "other", "", t0); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal("an answered request cannot be cancelled")
	}
}

func TestPublicRequestHidesCommands(t *testing.T) {
	r := answered(t)
	raw, _ := json.Marshal(PublicRequest(r))
	if strings.Contains(string(raw), "command_hashes") || len(r.Commands.IDs) == 0 {
		t.Fatalf("command bookkeeping must be hidden from the API: %s", raw)
	}
}

func TestConcurrentSignalsCreateOneRequestAndStaleSavesFail(t *testing.T) {
	store := NewMemoryStore()
	var wg sync.WaitGroup
	var mu sync.Mutex
	created, n := 0, 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, c, err := RegisterGapAtomically(store, gap("s1", "m7"), dialog(), func() string {
				mu.Lock()
				defer mu.Unlock()
				n++
				return fmt.Sprintf("req-%d", n)
			})
			if err != nil {
				t.Error(err)
			}
			if c {
				mu.Lock()
				created++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	all, _ := store.List(RequestFilter{})
	if created != 1 || len(all) != 1 {
		t.Fatalf("one request per key, got created=%d stored=%d", created, len(all))
	}
	r := all[0]
	next := ok(t)(Assign(r, coordinator, cmd(r, "a"), "spec-1", t0))
	if err := store.Save(next, r.Version); err != nil {
		t.Fatal(err)
	}
	stale := ok(t)(Assign(r, coordinator, cmd(r, "b"), "spec-2", t0))
	if err := store.Save(stale, r.Version); !errors.Is(err, ErrVersionConflict) {
		t.Fatal("a save based on a stale version must not overwrite another person's work")
	}
	if got, _ := store.Get(r.ID); got.AssigneeID != "spec-1" {
		t.Fatalf("first writer kept: %+v", got.AssigneeID)
	}
}
