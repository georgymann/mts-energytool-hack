package business

import (
	"energytool/backend/internal/domain"
	"strings"
	"testing"
	"time"
)

func TestCallbackChangeUpdatesCardAndExports(t *testing.T) {
	rec := newRecord(t)
	form := rec.Form
	form.CallbackMode = "scheduled"
	form.CallbackAt = "2030-11-14T14:30:00+03:00"
	session := domain.Session{ID: rec.SessionID, Application: &form}
	updated, _, err := SaveApplication(&rec, session, "", true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != rec.Version+1 || updated.Card.ContactTime.Status != Confirmed {
		t.Fatal("callback change not recorded")
	}
	if !strings.Contains(ExportTXT(updated.Card), "14:30") {
		t.Fatal("TXT lost callback time")
	}
	raw, err := ExportJSON(updated.Card)
	if err != nil || !strings.Contains(string(raw), "contact_time") || !strings.Contains(string(raw), "14:30") {
		t.Fatal("JSON lost callback time")
	}
}
