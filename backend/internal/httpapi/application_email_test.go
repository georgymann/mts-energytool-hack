package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"energytool/backend/internal/domain"
	"energytool/backend/internal/service"
	"energytool/backend/internal/store"
)

func TestEmailApplicationPersistsSeparatelyAndSurvivesModelResponse(t *testing.T) {
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
	send := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+session.ID+"/application", strings.NewReader(body)))
		return w
	}
	w := send(`{"email":"  Client.Name+meter@example.org  ","summary":"Нужен учёт воды"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("email-only save: %d %s", w.Code, w.Body)
	}
	var saved domain.Session
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Application == nil || saved.Application.Email != "Client.Name+meter@example.org" || saved.Application.Phone != "" || saved.Application.FullName != "" || saved.Application.Company != "" {
		t.Fatalf("email-only form lost or fabricated a contact: %+v", saved.Application)
	}
	if len(saved.Messages) != 0 || len(saved.Lead.Fields) != 0 {
		t.Fatal("reviewed contact must not invent model evidence")
	}
	if _, err := svc.SendMessage(context.Background(), session.ID, "Можно продолжить?"); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Get(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Application.Email != saved.Application.Email || got.Application.Phone != "" || got.Application.ID != saved.Application.ID {
		t.Fatal("email contact lost after model response/restart")
	}
	w = send(`{"phone":"+7 999 123 45 67","email":"Client.Name+meter@example.org","summary":"Нужен учёт воды"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("both contacts: %d %s", w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Application.Phone != "+7 999 123 45 67" || got.Application.Email != saved.Application.Email || got.Application.ID != saved.Application.ID {
		t.Fatal("adding phone must keep the separate email and application identity")
	}
}

func TestApplicationRejectsAbsentOrMalformedContacts(t *testing.T) {
	repo, err := store.NewFileStore(t.TempDir() + "/sessions.json")
	if err != nil {
		t.Fatal(err)
	}
	svc := service.New(repo, testML{})
	session, err := svc.CreateSession()
	if err != nil {
		t.Fatal(err)
	}
	handler := New(svc, nil, nil, time.Second).Handler()
	for _, body := range []string{
		`{"summary":"Учёт"}`, `{"phone":" ","email":" ","summary":"Учёт"}`,
		`{"email":"broken","summary":"Учёт"}`, `{"phone":"123","email":"valid@example.org","summary":"Учёт"}`,
		`{"phone":"+7 999 123 45 67","email":"broken","summary":"Учёт"}`,
		`{"email":"Name <valid@example.org>","summary":"Учёт"}`,
		`{"email":"valid@example.org\r\nBcc: other@example.org","summary":"Учёт"}`,
		`{"email":"valid@example.org","summary":" "}`,
		`{"email":"valid@example.org","summary":"Учёт","saved_at":"2020-01-01"}`,
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+session.ID+"/application", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Errorf("invalid payload accepted: %s -> %d", body, w.Code)
		}
	}
	got, err := svc.GetSession(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Application != nil {
		t.Fatal("invalid contact persisted")
	}
}
