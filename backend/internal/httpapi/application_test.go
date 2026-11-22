package httpapi

import (
	"context"
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

func TestReviewedApplicationPersistsAcrossRestartAndMLResponse(t *testing.T) {
	path := t.TempDir() + "/sessions.json"
	repo, err := store.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	app := service.New(repo, testML{})
	session, err := app.CreateSession()
	if err != nil {
		t.Fatal(err)
	}
	handler := New(app, nil, nil, time.Second).Handler()
	endpoint := "/api/v1/sessions/" + session.ID + "/application"
	request := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		handler.ServeHTTP(r, httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(body)))
		return r
	}
	for _, body := range []string{`{"phone":"123","summary":"Учёт"}`, `{"phone":"+7 999 123 45 67","summary":" "}`, `{"phone":"abc79991234567","summary":"Учёт"}`, `{"phone":"+7 999 123 45 67","summary":"Учёт","saved_at":"2020-01-01"}`} {
		if r := request(body); r.Code != 400 {
			t.Fatalf("invalid payload status=%d", r.Code)
		}
	}
	r := request(`{"phone":"+7 999 123 45 67","summary":"Нужен отдельный учёт по арендаторам."}`)
	if r.Code != 200 {
		t.Fatalf("save=%d %s", r.Code, r.Body)
	}
	var saved domain.Session
	if err := json.Unmarshal(r.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Application == nil || saved.Application.SavedAt.IsZero() || saved.Application.ID == "" {
		t.Fatal("missing confirmed application")
	}
	if len(saved.Messages) != 0 || len(saved.Lead.Fields) != 0 {
		t.Fatal("form must not invent model evidence or dialogue messages")
	}
	// A later model answer must not erase the reviewed form.
	if _, err := app.SendMessage(context.Background(), session.ID, "Можно продолжить?"); err != nil {
		t.Fatal(err)
	}
	r = request(`{"full_name":"Тестовый Клиент","phone":"+7 999 765 43 21","company":"Тест","summary":"Исправленная задача"}`)
	if r.Code != 200 {
		t.Fatalf("update=%d", r.Code)
	}
	repo2, err := store.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	restarted := service.New(repo2, testML{})
	got, err := restarted.GetSession(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Application.ID != saved.Application.ID {
		t.Fatal("application ID changed after edit/restart")
	}
	if got.Application.Phone != "+7 999 765 43 21" || got.Application.Summary != "Исправленная задача" {
		t.Fatal("application not persisted")
	}
	other, _ := restarted.CreateSession()
	if other.Application != nil {
		t.Fatal("application leaked to another session")
	}
	if _, err := app.CloseSession(session.ID); err != nil {
		t.Fatal(err)
	}
	if r := request(`{"phone":"+7 999 123 45 67","summary":"Учёт"}`); r.Code != 409 {
		t.Fatalf("closed session save=%d", r.Code)
	}
}
