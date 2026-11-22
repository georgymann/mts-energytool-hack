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

func TestClosedSessionCanResumeAndContinueThroughHTTP(t *testing.T) {
	repo, err := store.NewFileStore(t.TempDir() + "/sessions.json")
	if err != nil {
		t.Fatal(err)
	}
	app := service.New(repo, testML{})
	session, err := app.CreateSession()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.SendMessage(context.Background(), session.ID, "Первый вопрос"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.CloseSession(session.ID); err != nil {
		t.Fatal(err)
	}
	handler := New(app, nil, nil, time.Second).Handler()
	for i := 0; i < 2; i++ {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+session.ID+"/resume", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("resume status=%d: %s", response.Code, response.Body)
		}
		var resumed domain.Session
		if err := json.Unmarshal(response.Body.Bytes(), &resumed); err != nil {
			t.Fatal(err)
		}
		if resumed.Status != "active" || resumed.ClosedAt != nil || len(resumed.Messages) != 2 {
			t.Fatalf("resume lost the original session: %+v", resumed)
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+session.ID+"/messages", strings.NewReader(`{"content":"Продолжение","client_request_id":"continued"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("continuation status=%d: %s", response.Code, response.Body)
	}
	saved, err := app.GetSession(session.ID)
	if err != nil || len(saved.Messages) != 4 || saved.Messages[3].Response.RequestID != "continued" {
		t.Fatalf("continuation was not persisted: %+v, %v", saved, err)
	}
}

func TestPendingAndCanceledRequestHTTPErrorCodes(t *testing.T) {
	server := New(nil, nil, nil, time.Second)
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{service.ErrRequestPending, http.StatusConflict, "request_pending"},
		{context.DeadlineExceeded, http.StatusRequestTimeout, "request_interrupted"},
		{context.Canceled, http.StatusRequestTimeout, "request_interrupted"},
	} {
		response := httptest.NewRecorder()
		server.mapError(response, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/s/requests/r", nil), tc.err)
		var body domain.APIError
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if response.Code != tc.status || body.Code != tc.code {
			t.Fatalf("error %v: status=%d body=%+v", tc.err, response.Code, body)
		}
	}
}
