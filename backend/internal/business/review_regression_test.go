package business

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"energytool/backend/internal/domain"
)

func TestRequestIDRejectsChangedCommand(t *testing.T) {
	rec := newRecord(t)
	rec, _ = step(t, rec, ActionStartReview, "", "same-key")
	before, _ := json.Marshal(rec)
	if _, _, err := Transition(rec, ActionAccept, "", "", rec.Version, "same-key", t0); err == nil {
		t.Fatal("request ID reused for another action must conflict")
	}
	edits := []Edit{{Path: "contact.company", Action: "set", Value: "Первая компания"}}
	changed, err := EditCard(rec, rec.Version, ActorClient, edits, "edit-key", t0)
	if err != nil {
		t.Fatal(err)
	}
	edits[0].Value = "Другая компания"
	if _, err := EditCard(changed, rec.Version, ActorClient, edits, "edit-key", t0); err == nil {
		t.Fatal("request ID reused with changed edit body must conflict")
	}
	if _, err := EditCard(rec, rec.Version, ActorClient, edits, "same-key", t0); err == nil {
		t.Fatal("request ID must not be reused across operations")
	}
	after, _ := json.Marshal(rec)
	if string(before) != string(after) {
		t.Fatal("failed command mutated its input")
	}
}

func TestResaveCannotModifyDecidedApplication(t *testing.T) {
	for _, action := range []Action{ActionAccept, ActionReject} {
		rec := newRecord(t)
		rec, _ = step(t, rec, ActionStartReview, "", "start")
		rec, _ = step(t, rec, action, "other", "finish")
		session := fixtureSession()
		session.Application.Company = "Изменено после решения"
		got, events, err := SaveApplication(&rec, session, "ignored", true, t0)
		if !errors.Is(err, ErrApplicationClosed) || len(events) != 0 || got.Version != rec.Version {
			t.Fatalf("decided %s changed via form: %v", rec.Status, err)
		}
	}
}

func TestCardEditsKeepFormConsistent(t *testing.T) {
	rec := newRecord(t)
	edits := []Edit{{Path: "contact.phone", Action: "set", Value: "+7 999 111-22-33"}, {Path: "task", Action: "set", Value: "Уточнённая задача"}}
	updated, err := EditCard(rec, rec.Version, ActorClient, edits, "edit", t0)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Form.Phone != updated.Card.Contact.Phone.Value || updated.Form.Summary != updated.Card.Task.Value {
		t.Fatal("stored form diverges from edited card")
	}
	if rec.Form.Phone == updated.Form.Phone {
		t.Fatal("previous form mutated")
	}
}

func TestPowerConfirmationReplacesPreviousConfirmedValue(t *testing.T) {
	session := fixtureSession()
	session.Application.Summary = "Присоединённая мощность 900 кВт"
	card := build(t, session)
	target := -1
	for i, p := range card.Power {
		if p.Amount == "600" {
			target = i
		}
	}
	if target < 0 {
		t.Fatal("missing 600 fixture")
	}
	path := "power.1"
	if target != 1 {
		t.Fatal("unexpected fixture order")
	}
	next, _, err := ApplyEdits(card, card.Version, ActorClient, []Edit{{Path: path, Action: "confirm"}}, t0)
	if err != nil {
		t.Fatal(err)
	}
	actual := 0
	for _, p := range next.Power {
		if p.State == PowerActual {
			actual++
			if p.Amount != "600" {
				t.Fatal("old form value still actual")
			}
		}
	}
	if actual != 1 {
		t.Fatalf("actual count = %d", actual)
	}
}

func TestPowerKindsAreLocalToTheMeasurement(t *testing.T) {
	values := extractPower("Присоединённая мощность 900 кВт. Потребляем 600 кВт.")
	if len(values) != 2 || values[0].Kind.Value != "присоединённая" || values[1].Kind.Value != "потребление" {
		t.Fatalf("one kind incorrectly copied to another measurement: %+v", values)
	}
}

func TestConfirmedTaskEditUpdatesPower(t *testing.T) {
	card := build(t, fixtureSession())
	next, _, err := ApplyEdits(card, card.Version, ActorClient, []Edit{{Path: "task", Action: "set", Value: "Присоединённая мощность 600 кВт"}}, t0)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range next.Power {
		if p.Amount == "600" && p.State != PowerActual {
			t.Fatal("new confirmed task power not applied")
		}
		if p.Amount == "900" && p.State != PowerHistory {
			t.Fatal("old power not retained as history")
		}
	}
}

func TestDifferentPowerKindsRemainDistinct(t *testing.T) {
	values := extractPower("Присоединённая мощность 900 кВт. Потребляем 600 кВт.")
	values[0].Origin, values[1].Origin = OriginForm, OriginMessage
	resolvePower(values)
	if values[0].State != PowerActual || values[1].State != PowerActual {
		t.Fatal("different measurements must not replace each other")
	}
	for _, status := range []string{"technical_error", "out_of_scope", "clarification", "answered"} {
		if KnowledgeGap("s", "m", status, "", false, t0).ID != "" {
			t.Fatal("non-gap response created gap event")
		}
	}
}

func TestVerifiedEvidenceIsVerbatim(t *testing.T) {
	i := 0
	fields := verifiedFields([]quoted{{Value: "завод в туле", MessageIndex: &i}}, []domain.Message{{ID: "m1", Role: "user", Content: "Завод в Туле", Sequence: 1}})
	if len(fields) != 0 {
		t.Fatal("normalized text must not be presented as a verbatim quote")
	}
}

func TestEventAppendAfterTornTailSurvivesAnotherRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path, []byte(`{"id":"torn`), 0600); err != nil {
		t.Fatal(err)
	}
	log, err := OpenEventLog(path)
	if err != nil {
		t.Fatal(err)
	}
	event := SessionStarted("s-new", true, t0)
	if n, err := log.Append(event); err != nil || n != 1 {
		t.Fatalf("append: %d %v", n, err)
	}
	reopened, err := OpenEventLog(path)
	if err != nil || len(reopened.Events()) != 1 || reopened.Events()[0].ID != event.ID {
		t.Fatal("new event lost after damaged tail")
	}
}
