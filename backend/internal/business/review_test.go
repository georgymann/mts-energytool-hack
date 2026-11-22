package business

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newRecord(t *testing.T) ApplicationRecord {
	t.Helper()
	rec, events, err := SaveApplication(nil, fixtureSession(), "app-1", true, t0)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != StatusNew || rec.Version != 1 || rec.Card.Version != 1 || len(events) != 1 || events[0].Type != EventApplicationSaved {
		t.Fatalf("new application: %+v %+v", rec.Status, events)
	}
	return rec
}

func step(t *testing.T, rec ApplicationRecord, a Action, reason ReasonCode, req string) (ApplicationRecord, []Event) {
	t.Helper()
	out, events, err := Transition(rec, a, reason, "", rec.Version, req, t0.Add(time.Duration(rec.Version)*time.Minute))
	if err != nil {
		t.Fatalf("%s from %s: %v", a, rec.Status, err)
	}
	return out, events
}

func TestReviewLifecycle(t *testing.T) {
	rec := newRecord(t)
	if _, _, err := Transition(rec, ActionAccept, "", "", 1, "", t0); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("new -> accepted must be refused, got %v", err)
	}
	rec, ev := step(t, rec, ActionStartReview, "", "r1")
	if rec.Status != StatusInReview || ev[0].Type != EventReviewStarted {
		t.Fatal("start review")
	}
	if _, _, err := Transition(rec, ActionNeedsInfo, "", "", rec.Version, "", t0); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("needs_info requires a reason, got %v", err)
	}
	rec, ev = step(t, rec, ActionNeedsInfo, "missing_data", "r2")
	if rec.Status != StatusNeedsInfo || ev[0].Reason != "missing_data" {
		t.Fatalf("needs_info: %+v", ev)
	}
	rec, _ = step(t, rec, ActionStartReview, "", "r3")
	rec, ev = step(t, rec, ActionAccept, "", "r4")
	if rec.Status != StatusAccepted || ev[0].Type != EventAccepted || rec.Version != 5 || rec.Card.Version != 5 {
		t.Fatalf("accepted: %s v%d card v%d", rec.Status, rec.Version, rec.Card.Version)
	}
	if len(rec.Transitions) != 4 || rec.Transitions[1].From != StatusInReview || rec.Transitions[1].To != StatusNeedsInfo {
		t.Fatalf("transition history: %+v", rec.Transitions)
	}
	if _, err := EditCard(rec, rec.Version, ActorManager, []Edit{{Path: "task", Action: "set", Value: "x"}}, "", t0); !errors.Is(err, ErrApplicationClosed) {
		t.Fatalf("decided application is not editable, got %v", err)
	}
	if _, _, err := Transition(rec, ActionReopen, "", "", rec.Version, "", t0); !errors.Is(err, ErrReasonRequired) {
		t.Fatal("reopen requires a reason")
	}
	rec, ev = step(t, rec, ActionReopen, "manager_error", "r5")
	if rec.Status != StatusInReview || ev[0].Type != EventReviewStarted {
		t.Fatal("reopen returns to review")
	}
}

func TestRetriesAndStaleVersions(t *testing.T) {
	rec := newRecord(t)
	rec, _ = step(t, rec, ActionStartReview, "", "req-1")
	// Same request id again (even with a stale version): same outcome, no new event or version.
	again, ev, err := Transition(rec, ActionStartReview, "", "", 1, "req-1", t0)
	if err != nil || len(ev) != 0 || again.Version != rec.Version {
		t.Fatalf("retry must be a no-op: %v %d %v", err, len(ev), again.Version)
	}
	// Asking for the status it already has: no-op.
	if _, ev, err := Transition(rec, ActionStartReview, "", "", rec.Version, "other", t0); err != nil || len(ev) != 0 {
		t.Fatalf("repeated status must be a no-op: %v %d", err, len(ev))
	}
	// A different action with a stale version: explicit conflict.
	if _, _, err := Transition(rec, ActionAccept, "", "", 1, "req-2", t0); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale version must conflict, got %v", err)
	}
	edited, err := EditCard(rec, rec.Version, ActorManager, []Edit{{Path: "contact.company", Action: "set", Value: "ООО Тест"}}, "e-1", t0)
	if err != nil || edited.Version != rec.Version+1 || len(edited.PrevCards) != 1 || edited.PrevCards[0].Contact.Company.Status != Unknown {
		t.Fatalf("edit keeps previous card: %v %+v", err, edited.PrevCards)
	}
	if same, err := EditCard(edited, rec.Version, ActorManager, []Edit{{Path: "contact.company", Action: "set", Value: "ООО Тест"}}, "e-1", t0); err != nil || same.Version != edited.Version {
		t.Fatalf("retried edit must not apply twice: %v", err)
	}
}

func TestResaveKeepsHistoryAndStatus(t *testing.T) {
	rec := newRecord(t)
	rec, _ = step(t, rec, ActionStartReview, "", "")
	session := fixtureSession()
	same, ev, err := SaveApplication(&rec, session, "ignored", true, t0)
	if err != nil || len(ev) != 0 || same.Version != rec.Version {
		t.Fatalf("identical resave must be a no-op: %v %d", err, len(ev))
	}
	session.Application.Company = "ООО Новое"
	updated, ev, err := SaveApplication(&rec, session, "ignored", true, t0.Add(time.Hour))
	if err != nil || len(ev) != 1 || updated.ID != "app-1" || updated.Version != rec.Version+1 {
		t.Fatalf("changed form updates the same application: %v %+v", err, updated)
	}
	if updated.Status != StatusInReview || len(updated.PrevCards) != 1 || updated.Card.Contact.Company.Value != "ООО Новое" {
		t.Fatalf("status kept, previous card kept: %+v", updated)
	}
	other := fixtureSession()
	other.ID = "s2"
	if _, _, err := SaveApplication(&rec, other, "x", true, t0); err == nil {
		t.Fatal("application must not move to another session")
	}
}

func TestEventLogDeduplicatesAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := OpenEventLog(path)
	if err != nil {
		t.Fatal(err)
	}
	e1 := SessionStarted("s1", false, t0)
	e2 := ConsultationRequested("s1", "m1", false, t0.Add(time.Second))
	if n, _ := log.Append(e1, e2, e1); n != 2 {
		t.Fatalf("duplicate in one batch must be dropped, appended %d", n)
	}
	raw, err := os.ReadFile(path)
	if err != nil || strings.HasPrefix(string(raw), "\n") || strings.Contains(string(raw), "\n\n") {
		t.Fatal("normal JSONL export must not contain blank records")
	}
	if n, _ := log.Append(e2); n != 0 {
		t.Fatal("replayed event must be ignored")
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"id":"torn`) // simulate a crash mid-write
	f.Close()
	reopened, err := OpenEventLog(path)
	if err != nil || len(reopened.Events()) != 2 {
		t.Fatalf("reload must skip the torn line: %v %d", err, len(reopened.Events()))
	}
	if n, _ := reopened.Append(e1); n != 0 {
		t.Fatal("dedup must survive restart")
	}
}

func TestViewsHideInternalsAndPhone(t *testing.T) {
	rec := newRecord(t)
	rec, _ = step(t, rec, ActionStartReview, "", "req-x")
	if PublicView(rec).RequestIDs != nil || rec.RequestIDs == nil {
		t.Fatal("public view must drop request ids without touching the record")
	}
	item := NewListItem(rec)
	raw, _ := json.Marshal(item)
	if strings.Contains(string(raw), "900 000") || item.Company != nil || *item.ContactName != "Иван Тестовый" ||
		item.StatusLabel != "в работе" || len(item.Products) != 2 || item.UnknownsCount != len(rec.Card.Unknowns) {
		t.Fatalf("list item: %s", raw)
	}
}
