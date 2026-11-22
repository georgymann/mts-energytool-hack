package service

import (
	"context"
	"energytool/backend/internal/domain"
	"testing"
)

func TestSummaryPassedForwardAndStoredPerAnswer(t *testing.T) {
	ml := &fakeML{fn: func(req domain.ConsultationRequest) (domain.ConsultationResult, error) {
		r := result(req.SessionID, "Ответ", domain.LeadState{SessionID: req.SessionID, Fields: map[string]string{}, Evidence: map[string]interface{}{}})
		r.Summary = map[string]interface{}{"state_version": 2, "parameters": []interface{}{map[string]interface{}{"value": "Показания снимаем вручную", "message_index": 0}}}
		return r, nil
	}}
	app := newTestService(t, ml)
	first, _ := app.CreateSession()
	second, _ := app.CreateSession()
	for _, request := range []struct{ id, text string }{{first.ID, "Показания снимаем вручную"}, {first.ID, "Раз в месяц"}, {second.ID, "Новая задача"}} {
		if _, err := app.SendMessage(context.Background(), request.id, request.text); err != nil {
			t.Fatal(err)
		}
	}
	if ml.calls[1].SummarySoFar["state_version"] == nil {
		t.Fatal("confirmed summary not forwarded")
	}
	if len(ml.calls[2].SummarySoFar) != 0 {
		t.Fatal("summary leaked into another session")
	}
	stored, err := app.store.Get(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Messages[1].Response.Summary["state_version"] == nil {
		t.Fatal("response summary not persisted for reload and plan source")
	}
}

func TestGenerationFailureKeepsNewEvidenceBackedFacts(t *testing.T) {
	ml := &fakeML{fn: func(req domain.ConsultationRequest) (domain.ConsultationResult, error) {
		r := result(req.SessionID, "Ответ", domain.LeadState{SessionID: req.SessionID, Fields: map[string]string{}, Evidence: map[string]interface{}{}})
		values := []interface{}{map[string]interface{}{"value": "Расходы 30000 рублей", "message_index": 0}}
		if len(req.Messages) > 1 {
			r.Status = "technical_error"
			values = append(values, map[string]interface{}{"value": "Есть приборы учёта", "message_index": 2})
		}
		r.Summary = map[string]interface{}{"state_version": float64(2), "parameters": values}
		return r, nil
	}}
	app := newTestService(t, ml)
	session, _ := app.CreateSession()
	for _, text := range []string{"Расходы 30000 рублей", "Есть приборы учёта"} {
		if _, err := app.SendMessage(context.Background(), session.ID, text); err != nil {
			t.Fatal(err)
		}
	}
	stored, _ := app.GetSession(session.ID)
	if len(stored.LastResult.Summary["parameters"].([]interface{})) != 2 {
		t.Fatal("generation failure discarded newly confirmed user facts")
	}
}
