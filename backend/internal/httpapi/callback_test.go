package httpapi

import (
	"encoding/json"
	"energytool/backend/internal/domain"
	"energytool/backend/internal/service"
	"energytool/backend/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCallbackPreferenceValidationAndPersistence(t *testing.T) {
	path := t.TempDir() + "/sessions.json"
	repo, err := store.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	svc := service.New(repo, testML{})
	session, err := svc.CreateSession()
	if err != nil {
		t.Fatal(err)
	}
	handler := New(svc, nil, nil, time.Second).Handler()
	send := func(mode, at string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"phone": "+7 999 000 00 00", "summary": "Тест выбора времени", "callback_mode": mode, "callback_at": at})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+session.ID+"/application", strings.NewReader(string(body))))
		return w
	}
	future := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Minute)
	for _, tc := range [][2]string{{"scheduled", ""}, {"scheduled", "broken"}, {"scheduled", time.Now().Add(-time.Hour).Format(time.RFC3339)}, {"asap", future.Format(time.RFC3339)}, {"unknown", ""}} {
		if w := send(tc[0], tc[1]); w.Code != 400 {
			t.Fatalf("invalid preference accepted: %d", w.Code)
		}
	}
	if w := send("scheduled", future.Format(time.RFC3339)); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	restarted, err := store.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := restarted.Get(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	at, err := time.Parse(time.RFC3339, got.Application.CallbackAt)
	if err != nil || !at.Equal(future) || got.Application.CallbackMode != "scheduled" || !strings.Contains(got.Application.ContactPreference(), "МСК") {
		t.Fatal("preference lost or timezone changed")
	}
	id := got.Application.ID
	w := send("asap", "")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	var updated domain.Session
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Application.ID != id || updated.Application.CallbackAt != "" || updated.Application.CallbackMode != "asap" {
		t.Fatal("ASAP did not clear scheduled time or changed ID")
	}
}
