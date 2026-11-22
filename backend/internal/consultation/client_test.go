package consultation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"energytool/backend/internal/domain"
)

func TestHTTPClientUsesPythonContractAndValidatesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/respond" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		var request domain.ConsultationRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.SessionID != "session-1" || len(request.Messages) != 1 {
			t.Errorf("request payload: %+v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(domain.ConsultationResult{Answer: "Ответ", Status: "answered", Lead: domain.LeadState{SessionID: "session-1"}, SchemaVersion: "1.0"})
	}))
	defer server.Close()
	client, err := NewHTTPClient(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Respond(context.Background(), domain.ConsultationRequest{SessionID: "session-1", Messages: []domain.MessageInput{{Role: "user", Content: "Вопрос"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Answer != "Ответ" || got.Sources == nil || got.Summary == nil || got.Debug == nil {
		t.Fatalf("response defaults not normalized: %+v", got)
	}
}

func TestHTTPClientRejectsMismatchedSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(domain.ConsultationResult{Answer: "Ответ", Status: "answered", Lead: domain.LeadState{SessionID: "other"}, SchemaVersion: "1.0"})
	}))
	defer server.Close()
	client, _ := NewHTTPClient(server.URL, time.Second)
	_, err := client.Respond(context.Background(), domain.ConsultationRequest{SessionID: "wanted"})
	if err == nil {
		t.Fatal("expected session mismatch error")
	}
}
