package business

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestEmailOnlyCardExportsContactAndUsesEmailNextStep(t *testing.T) {
	session := fixtureSession()
	session.Application.Phone = ""
	session.Application.Email = "Client+meter@example.org"
	session.Application.FullName = ""
	card := build(t, session)
	if card.Contact.Email.Value != session.Application.Email || card.Contact.Email.Status != Confirmed || card.Contact.Phone.Status != Unknown || card.Contact.PreferredChannel.Value != "email" {
		t.Fatalf("wrong email contact: %+v", card.Contact)
	}
	if !strings.HasPrefix(card.NextStep.Value, "Написать по электронной почте") || strings.Contains(card.NextStep.Value, "Позвонить") {
		t.Fatalf("wrong next step: %s", card.NextStep.Value)
	}
	if !strings.Contains(ExportTXT(card), "Email: Client+meter@example.org [подтверждено]") {
		t.Fatal("text handoff lost email")
	}
	raw, err := ExportJSON(card)
	if err != nil {
		t.Fatal(err)
	}
	var roundtrip NeedCard
	if err := json.Unmarshal(raw, &roundtrip); err != nil {
		t.Fatal(err)
	}
	if roundtrip.Contact.Email.Value != card.Contact.Email.Value || roundtrip.Contact.Email.Status != card.Contact.Email.Status {
		t.Fatal("JSON handoff lost email")
	}
	session.Application.Phone = "+7 999 123 45 67"
	if both := build(t, session); both.Contact.PreferredChannel.Value != "телефон" || both.Contact.Email.Value != card.Contact.Email.Value {
		t.Fatal("phone should remain preferred without discarding email")
	}
}

func TestEmailFormResaveAndCardEditsStayConsistent(t *testing.T) {
	rec := newRecord(t)
	session := fixtureSession()
	session.Application.Email = "first@example.org"
	updated, events, err := SaveApplication(&rec, session, "ignored", true, t0.Add(time.Minute))
	if err != nil || updated.Version != rec.Version+1 || len(events) != 1 || updated.Card.Contact.Email.Value != "first@example.org" || updated.Form.Email != "first@example.org" {
		t.Fatalf("email-only change not saved: %v %+v", err, updated.Form)
	}
	same, events, err := SaveApplication(&updated, session, "ignored", true, t0.Add(2*time.Minute))
	if err != nil || same.Version != updated.Version || len(events) != 0 {
		t.Fatal("identical email form must stay a no-op")
	}
	edited, err := EditCard(updated, updated.Version, ActorClient, []Edit{{Path: "contact.phone", Action: "remove"}, {Path: "contact.email", Action: "set", Value: "corrected@example.org"}}, "contact-edit", t0.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if edited.Form.Phone != "" || edited.Form.Email != "corrected@example.org" || edited.Card.Contact.Email.Value != edited.Form.Email || edited.Card.Contact.PreferredChannel.Value != "email" || !strings.HasPrefix(edited.Card.NextStep.Value, "Написать по электронной почте") {
		t.Fatalf("form/card mismatch: %+v %+v", edited.Form, edited.Card.Contact)
	}
	if updated.Form.Email != "first@example.org" || updated.Form.Phone == "" {
		t.Fatal("old version mutated")
	}
	for _, edits := range [][]Edit{
		{{Path: "contact.email", Action: "remove"}},
		{{Path: "contact.phone", Action: "set", Value: "+7 999 123 45 67"}, {Path: "contact.email", Action: "set", Value: "broken"}},
	} {
		got, err := EditCard(edited, edited.Version, ActorClient, edits, "bad-edit", t0)
		if !errors.Is(err, ErrInvalidEdit) || got.Form.Email != edited.Form.Email || got.Form.Phone != "" || got.Version != edited.Version {
			t.Fatal("invalid/removing last contact edit was not atomic")
		}
	}
	// Both orders of a contact replacement must work as one atomic batch.
	replaced, err := EditCard(edited, edited.Version, ActorClient, []Edit{{Path: "contact.email", Action: "remove"}, {Path: "contact.phone", Action: "set", Value: "+7 999 123 45 67"}}, "replace-email", t0)
	if err != nil || replaced.Form.Email != "" || replaced.Card.Contact.PreferredChannel.Value != "телефон" {
		t.Fatalf("contact replacement failed: %v", err)
	}
	queue, _ := json.Marshal(NewListItem(edited))
	if strings.Contains(string(queue), "corrected@example.org") {
		t.Fatal("queue leaked contact email")
	}
}
