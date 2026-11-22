package service

import (
	"context"
	"testing"

	"energytool/backend/internal/domain"
	"energytool/backend/internal/expert"
)

func TestResumeClosedSessionKeepsSpecialistAnswer(t *testing.T) {
	app := newTestService(t, &fakeML{fn: func(req domain.ConsultationRequest) (domain.ConsultationResult, error) {
		return domain.ConsultationResult{Status: "knowledge_gap", Answer: "Уточним у специалиста.", Lead: domain.LeadState{SessionID: req.SessionID}}, nil
	}})
	session, err := app.CreateSession()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.SendMessage(context.Background(), session.ID, "Подтвердите интеграцию с неизвестной системой"); err != nil {
		t.Fatal(err)
	}
	requests, err := app.ListExpertRequests(expert.RequestFilter{SessionID: session.ID})
	if err != nil || len(requests) != 1 {
		t.Fatalf("expert request: %v %+v", err, requests)
	}
	if _, err := app.CloseSession(session.ID); err != nil {
		t.Fatal(err)
	}
	r, err := app.AssignExpert(requests[0].ID, expert.Actor{ID: "dispatcher", Role: expert.RoleCoordinator}, expert.Command{ExpectedVersion: requests[0].Version, ID: "assign"}, "specialist")
	if err != nil {
		t.Fatal(err)
	}
	actor := expert.Actor{ID: "specialist", Role: expert.RoleSpecialist}
	r, err = app.StartExpert(r.ID, actor, expert.Command{ExpectedVersion: r.Version, ID: "start"})
	if err != nil {
		t.Fatal(err)
	}
	command := expert.Command{ExpectedVersion: r.Version, ID: "answer"}
	const answer = "Для подтверждения нужны модель счётчика и документация интерфейса."
	r, err = app.AnswerExpert(r.ID, actor, command, "resume-reply", answer)
	if err != nil || !r.Reply.ToClosedSession {
		t.Fatalf("answer to closed session: %v %+v", err, r)
	}
	closed, err := app.GetSession(session.ID)
	if err != nil || closed.Status != "closed" || len(closed.Messages) != 3 {
		t.Fatalf("closed history is incomplete: %v %+v", err, closed)
	}
	for attempt := 0; attempt < 2; attempt++ {
		resumed, err := app.ResumeSession(session.ID)
		if err != nil || resumed.Status != "active" || resumed.ClosedAt != nil || len(resumed.Messages) != 3 {
			t.Fatalf("resume lost or duplicated reply: %v %+v", err, resumed)
		}
		message := resumed.Messages[2]
		if message.Content != answer || message.Response == nil || message.Response.Origin != "specialist" || message.Response.ReplyID != "resume-reply" {
			t.Fatalf("resume lost exact answer/attribution: %+v", message)
		}
		if _, err := app.AnswerExpert(r.ID, actor, command, "resume-reply", answer); err != nil {
			t.Fatalf("replayed reply after resume: %v", err)
		}
	}
}
