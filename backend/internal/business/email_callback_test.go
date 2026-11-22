package business

import (
	"strings"
	"testing"
	"time"
)

func TestEmailOnlyCardDoesNotConfirmTelephoneCallbackTime(t *testing.T) {
	for _, mode := range []string{"asap", "scheduled"} {
		t.Run(mode, func(t *testing.T) {
			session := fixtureSession()
			session.Application.Phone = ""
			session.Application.Email = "audit-ui@example.test"
			session.Application.CallbackMode = mode
			if mode == "scheduled" {
				session.Application.CallbackAt = "2030-11-14T14:30:00+03:00"
			}
			card := build(t, session)
			if card.ContactTime.Value != "" || card.ContactTime.Status != Unknown {
				t.Fatalf("email-only card claims a callback time: %+v", card.ContactTime)
			}
			if text := ExportTXT(card); strings.Contains(text, "Как можно скорее") || strings.Contains(text, "14:30") {
				t.Fatalf("email-only export retained a telephone callback: %s", text)
			}
		})
	}
}

func TestLegacyEmailCallbackDisplayIsNormalizedWithoutBusinessChange(t *testing.T) {
	session := fixtureSession()
	session.Application.Phone = ""
	session.Application.Email = "audit-ui@example.test"
	session.Application.CallbackMode = "asap"
	rec, _, err := SaveApplication(nil, session, "app-email", true, t0)
	if err != nil {
		t.Fatal(err)
	}
	rec.Status = StatusAccepted
	rec.Card.ContactTime = formField(session.Application.ContactPreference())
	updated, events, err := SaveApplication(&rec, session, "", true, t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Card.ContactTime.Value != "" || updated.Card.ContactTime.Status != Unknown {
		t.Fatalf("legacy callback was retained: %+v", updated.Card.ContactTime)
	}
	if updated.Version != rec.Version || updated.Status != rec.Status || !updated.UpdatedAt.Equal(rec.UpdatedAt) || len(events) != 0 {
		t.Fatal("display normalization changed application workflow or metrics")
	}
	if rec.Card.ContactTime.Value == "" {
		t.Fatal("normalization mutated the caller's previous record")
	}
}

func TestReplacingPhoneWithEmailClearsOldCallbackPreference(t *testing.T) {
	session := fixtureSession()
	session.Application.CallbackMode = "scheduled"
	session.Application.CallbackAt = "2030-11-14T14:30:00+03:00"
	rec, _, err := SaveApplication(nil, session, "app-contact", true, t0)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := EditCard(rec, rec.Version, ActorManager, []Edit{
		{Path: "contact.phone", Action: "remove"},
		{Path: "contact.email", Action: "set", Value: "audit-ui@example.test"},
	}, "replace-contact", t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Card.ContactTime.Value != "" || updated.Form.CallbackMode != "" || updated.Form.CallbackAt != "" {
		t.Fatal("replacing phone with email kept the old callback")
	}
	if rec.Card.ContactTime.Value == "" || rec.Form.CallbackAt == "" {
		t.Fatal("contact edit mutated the original record")
	}
}

func TestExplicitEmailContactTimeSurvivesUnrelatedEditAndResave(t *testing.T) {
	session := fixtureSession()
	session.Application.Phone = ""
	session.Application.Email = "audit-ui@example.test"
	session.Application.CallbackMode = "asap"
	rec, _, err := SaveApplication(nil, session, "app-email-time", true, t0)
	if err != nil {
		t.Fatal(err)
	}
	explicit := Field{Value: "Писать по будням после 16:00", Status: Confirmed, Origin: OriginManager}
	rec.Card.ContactTime = explicit
	unchanged, events, err := SaveApplication(&rec, session, "", true, t0.Add(time.Minute))
	if err != nil || len(events) != 0 || unchanged.Card.ContactTime.Value != explicit.Value || unchanged.Card.ContactTime.Origin != explicit.Origin {
		t.Fatalf("identical form erased an explicit email preference: %v %+v", err, unchanged.Card.ContactTime)
	}
	updated, err := EditCard(rec, rec.Version, ActorManager, []Edit{{Path: "contact.company", Action: "set", Value: "Новая компания"}}, "company-only", t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Card.ContactTime.Value != explicit.Value || updated.Card.ContactTime.Origin != explicit.Origin || updated.Card.ContactTime.Status != Confirmed {
		t.Fatalf("unrelated edit erased an explicit email preference: %+v", updated.Card.ContactTime)
	}
}
