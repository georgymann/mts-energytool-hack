package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"energytool/backend/internal/domain"
	"energytool/backend/internal/expert"
	"energytool/backend/internal/service"
	"energytool/backend/internal/store"
)

type SessionService interface {
	CreateSession() (domain.Session, error)
	SaveApplication(string, domain.Application) (domain.Session, error)
	SaveApplicationWithID(string, string, domain.Application) (domain.Session, error)
	SendMessageWithID(context.Context, string, string, string) (domain.ConsultationResult, error)
	FindRequest(string, string) (domain.ConsultationResult, error)
	GetSession(string) (domain.Session, error)
	ListSessions() ([]domain.Session, error)
	CloseSession(string) (domain.Session, error)
	ResumeSession(string) (domain.Session, error)
	Lead(string) (domain.LeadState, error)
	SendMessage(context.Context, string, string) (domain.ConsultationResult, error)
	ListExpertRequests(expert.RequestFilter) ([]expert.Request, error)
	GetExpertRequest(string) (expert.Request, error)
	CreateKnowledge(expert.Actor, string, expert.DraftInput, bool) (expert.KnowledgeEntry, error)
	GetKnowledge(string) (expert.KnowledgeEntry, error)
	ListKnowledge() ([]expert.KnowledgeEntry, error)
	ApproveKnowledge(string, expert.Actor, expert.Command, int, expert.ReviewChecklist) (expert.KnowledgeEntry, error)
	RevokeKnowledge(string, expert.Actor, expert.Command, string) (expert.KnowledgeEntry, error)
	AssignExpert(string, expert.Actor, expert.Command, string) (expert.Request, error)
	StartExpert(string, expert.Actor, expert.Command) (expert.Request, error)
	AnswerExpert(string, expert.Actor, expert.Command, string, string) (expert.Request, error)
	CancelExpert(string, expert.Actor, expert.Command, expert.CancelReason, string) (expert.Request, error)
}

type Readiness func(context.Context) error

type Server struct {
	service       SessionService
	ready         Readiness
	logger        *slog.Logger
	timeout       time.Duration
	operatorToken string
}

func New(service SessionService, ready Readiness, logger *slog.Logger, requestTimeout time.Duration, operatorToken ...string) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if requestTimeout <= 0 {
		requestTimeout = 100 * time.Second
	}
	token := ""
	if len(operatorToken) > 0 {
		token = operatorToken[0]
	}
	return &Server{service: service, ready: ready, logger: logger, timeout: requestTimeout, operatorToken: token}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/applications", s.listApplications)
	mux.HandleFunc("GET /api/v1/applications/reasons", s.applicationReasons)
	mux.HandleFunc("GET /api/v1/applications/{applicationID}", s.getApplication)
	mux.HandleFunc("PATCH /api/v1/applications/{applicationID}/card", s.editApplication)
	mux.HandleFunc("POST /api/v1/applications/{applicationID}/transitions", s.transitionApplication)
	mux.HandleFunc("GET /api/v1/applications/{applicationID}/export", s.exportApplication)
	mux.HandleFunc("GET /api/v1/metrics/pilot", s.pilotMetrics)

	mux.HandleFunc("GET /health/live", s.live)
	mux.HandleFunc("GET /health/ready", s.readiness)
	mux.HandleFunc("POST /api/v1/sessions", s.createSession)
	mux.HandleFunc("GET /api/v1/sessions", s.listSessions)
	mux.HandleFunc("GET /api/v1/sessions/{sessionID}", s.getSession)
	mux.HandleFunc("POST /api/v1/sessions/{sessionID}/close", s.closeSession)
	mux.HandleFunc("POST /api/v1/sessions/{sessionID}/resume", s.resumeSession)
	mux.HandleFunc("GET /api/v1/sessions/{sessionID}/messages", s.getMessages)
	mux.HandleFunc("POST /api/v1/sessions/{sessionID}/messages", s.postMessage)
	mux.HandleFunc("GET /api/v1/sessions/{sessionID}/requests/{requestID}", s.getRequest)
	mux.HandleFunc("GET /api/v1/sessions/{sessionID}/lead", s.getLead)
	mux.HandleFunc("POST /api/v1/sessions/{sessionID}/application", s.saveApplication)
	mux.HandleFunc("GET /api/v1/sessions/{sessionID}/expert-requests", s.sessionExpertRequests)
	mux.HandleFunc("GET /api/v1/operator/expert-requests", s.operatorExpertRequests)
	mux.HandleFunc("GET /api/v1/operator/expert-requests/{requestID}", s.operatorExpertRequest)
	mux.HandleFunc("POST /api/v1/operator/expert-requests/{requestID}/assign", s.assignExpert)
	mux.HandleFunc("POST /api/v1/operator/expert-requests/{requestID}/start", s.startExpert)
	mux.HandleFunc("POST /api/v1/operator/expert-requests/{requestID}/reply", s.replyExpert)
	mux.HandleFunc("POST /api/v1/operator/expert-requests/{requestID}/cancel", s.cancelExpert)
	mux.HandleFunc("POST /api/v1/operator/knowledge", s.createKnowledge)
	mux.HandleFunc("GET /api/v1/operator/knowledge", s.listKnowledge)
	mux.HandleFunc("GET /api/v1/operator/knowledge/{knowledgeID}", s.getKnowledge)
	mux.HandleFunc("POST /api/v1/operator/knowledge/{knowledgeID}/approve", s.approveKnowledge)
	mux.HandleFunc("POST /api/v1/operator/knowledge/{knowledgeID}/revoke", s.revokeKnowledge)
	return requestID(recoverer(s.logger, mux))
}

func (s *Server) live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) readiness(w http.ResponseWriter, r *http.Request) {
	if s.ready != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := s.ready(ctx); err != nil {
			writeError(w, r, http.StatusServiceUnavailable, "not_ready", "Required dependency is unavailable")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	if !emptyBody(w, r) {
		return
	}
	session, err := s.service.CreateSession()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, session)
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	if !emptyBody(w, r) {
		return
	}
	sessions, err := s.service.ListSessions()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"sessions": sessions})
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	if !emptyBody(w, r) {
		return
	}
	session, err := s.service.GetSession(r.PathValue("sessionID"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) closeSession(w http.ResponseWriter, r *http.Request) {
	if !emptyBody(w, r) {
		return
	}
	session, err := s.service.CloseSession(r.PathValue("sessionID"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) resumeSession(w http.ResponseWriter, r *http.Request) {
	if !emptyBody(w, r) {
		return
	}
	session, err := s.service.ResumeSession(r.PathValue("sessionID"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) getMessages(w http.ResponseWriter, r *http.Request) {
	if !emptyBody(w, r) {
		return
	}
	session, err := s.service.GetSession(r.PathValue("sessionID"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"messages": session.Messages})
}

func (s *Server) postMessage(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	var payload struct {
		Content         string `json:"content"`
		ClientRequestID string `json:"client_request_id"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		status, code, message := http.StatusBadRequest, "invalid_json", "Request body must be valid JSON"
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			status, code, message = http.StatusRequestEntityTooLarge, "body_too_large", "Request body is too large"
		}
		writeError(w, r, status, code, message)
		return
	}
	if err := ensureEOF(decoder); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_json", "Request must contain one JSON object")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.timeout)
	defer cancel()
	requestID := payload.ClientRequestID
	if requestID == "" {
		requestID = r.Header.Get("Idempotency-Key")
	}
	result, err := s.service.SendMessageWithID(ctx, r.PathValue("sessionID"), requestID, payload.Content)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) getLead(w http.ResponseWriter, r *http.Request) {
	if !emptyBody(w, r) {
		return
	}
	lead, err := s.service.Lead(r.PathValue("sessionID"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, lead)
}

func (s *Server) mapError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidApplication):
		writeError(w, r, http.StatusBadRequest, "invalid_application", "Укажите корректный телефон или email и краткое описание задачи.")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "Session was not found")
	case errors.Is(err, service.ErrClosed):
		writeError(w, r, http.StatusConflict, "session_closed", "Session is closed")
	case errors.Is(err, service.ErrHistoryLimit):
		writeError(w, r, http.StatusRequestEntityTooLarge, "history_limit", "Session history limit reached")
	case errors.Is(err, service.ErrMessageTooLong):
		writeError(w, r, http.StatusRequestEntityTooLarge, "message_too_long", "Message exceeds 10000 characters")
	case errors.Is(err, service.ErrEmptyMessage):
		writeError(w, r, http.StatusBadRequest, "empty_message", "Message content is required")
	case errors.Is(err, service.ErrRequestConflict):
		writeError(w, r, http.StatusConflict, "request_id_conflict", "client_request_id was already used with different data")
	case errors.Is(err, service.ErrRequestNotFound):
		writeError(w, r, http.StatusNotFound, "request_not_found", "Request result was not found")
	case errors.Is(err, service.ErrRequestPending):
		writeError(w, r, http.StatusConflict, "request_pending", "Request is still being processed")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		writeError(w, r, http.StatusRequestTimeout, "request_interrupted", "Request was interrupted; refresh the session before retrying")
	case errors.Is(err, expert.ErrForbidden):
		writeError(w, r, http.StatusForbidden, "forbidden", "Operator access required")
	case errors.Is(err, expert.ErrVersionConflict):
		writeError(w, r, http.StatusConflict, "version_conflict", "The request has changed; reload it")
	case errors.Is(err, expert.ErrRequestConflict):
		writeError(w, r, http.StatusConflict, "command_conflict", "command_id was already used with different data")
	default:
		s.internalError(w, r, err)
	}
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	s.logger.Error("backend request failed", "request_id", requestIDFrom(r.Context()), "error_type", fmt.Sprintf("%T", err))
	writeError(w, r, http.StatusInternalServerError, "internal_error", "Request could not be completed")
}

func emptyBody(w http.ResponseWriter, r *http.Request) bool {
	if r.Body == nil {
		return true
	}
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil || len(data) != 0 {
		writeError(w, r, http.StatusBadRequest, "unexpected_body", "This endpoint does not accept a body")
		return false
	}
	return true
}

func ensureEOF(decoder *json.Decoder) error {
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("unexpected trailing JSON")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(w, status, domain.APIError{Code: code, Message: message, RequestID: requestIDFrom(r.Context())})
}

func (s *Server) saveApplication(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var payload struct {
		ClientRequestID string `json:"client_request_id"`
		Email           string `json:"email"`
		CallbackMode    string `json:"callback_mode"`
		CallbackAt      string `json:"callback_at"`
		FullName        string `json:"full_name"`
		Phone           string `json:"phone"`
		Company         string `json:"company"`
		Summary         string `json:"summary"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_json", "Некорректные данные заявки.")
		return
	}
	if err := ensureEOF(decoder); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_json", "Некорректные данные заявки.")
		return
	}
	requestID := r.Header.Get("Idempotency-Key")
	if requestID == "" {
		requestID = payload.ClientRequestID
	}
	if requestID == "" {
		requestID = r.URL.Query().Get("client_request_id")
	}
	session, err := s.service.SaveApplicationWithID(r.PathValue("sessionID"), requestID, domain.Application{CallbackMode: payload.CallbackMode, CallbackAt: payload.CallbackAt, FullName: payload.FullName, Phone: payload.Phone, Email: payload.Email, Company: payload.Company, Summary: payload.Summary})
	if err != nil {
		s.applicationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) getRequest(w http.ResponseWriter, r *http.Request) {
	result, err := s.service.FindRequest(r.PathValue("sessionID"), r.PathValue("requestID"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func actorFrom(r *http.Request) expert.Actor {
	role := expert.Role(r.Header.Get("X-Operator-Role"))
	return expert.Actor{ID: r.Header.Get("X-Operator-ID"), Role: role}
}
func (s *Server) trustedActor(r *http.Request) (expert.Actor, bool) {
	a := actorFrom(r)
	if a.ID == "" || s.operatorToken == "" || r.Header.Get("X-Operator-Token") != s.operatorToken {
		return expert.Actor{}, false
	}
	switch a.Role {
	case expert.RoleCoordinator, expert.RoleSpecialist, expert.RoleReviewer:
		return a, true
	}
	return expert.Actor{}, false
}
func commandFrom(r *http.Request, body struct {
	ExpectedVersion int    `json:"expected_version"`
	CommandID       string `json:"command_id"`
}) expert.Command {
	return expert.Command{ExpectedVersion: body.ExpectedVersion, ID: body.CommandID}
}
func (s *Server) sessionExpertRequests(w http.ResponseWriter, r *http.Request) {
	items, err := s.service.ListExpertRequests(expert.RequestFilter{SessionID: r.PathValue("sessionID")})
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"requests": items})
}
func (s *Server) operatorExpertRequests(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.trustedActor(r); !ok {
		writeError(w, r, 403, "forbidden", "Operator access required")
		return
	}
	items, err := s.service.ListExpertRequests(expert.RequestFilter{Status: expert.RequestStatus(r.URL.Query().Get("status")), AssigneeID: r.URL.Query().Get("assignee_id")})
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"requests": items})
}
func (s *Server) operatorExpertRequest(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.trustedActor(r); !ok {
		writeError(w, r, 403, "forbidden", "Operator access required")
		return
	}
	item, err := s.service.GetExpertRequest(r.PathValue("requestID"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, 200, item)
}
func (s *Server) operatorCommand(w http.ResponseWriter, r *http.Request, action func(expert.Actor, expert.Command, map[string]interface{}) (expert.Request, error)) {
	actor, trusted := s.trustedActor(r)
	if !trusted {
		writeError(w, r, 403, "forbidden", "Operator access required")
		return
	}
	var raw map[string]interface{}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&raw); err != nil {
		writeError(w, r, 400, "invalid_json", "Invalid command")
		return
	}
	version, _ := raw["expected_version"].(float64)
	id, _ := raw["command_id"].(string)
	item, err := s.service.GetExpertRequest(r.PathValue("requestID"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out, err := action(actor, expert.Command{ExpectedVersion: int(version), ID: id}, raw)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	_ = item
	writeJSON(w, 200, out)
}

func (s *Server) createKnowledge(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.trustedActor(r)
	if !ok {
		writeError(w, r, 403, "forbidden", "Operator access required")
		return
	}
	var body struct {
		SourceRequestID string `json:"source_request_id"`
		Test            bool   `json:"test"`
		expert.DraftInput
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 24<<10)).Decode(&body); err != nil {
		writeError(w, r, 400, "invalid_json", "Invalid knowledge draft")
		return
	}
	e, err := s.service.CreateKnowledge(actor, body.SourceRequestID, body.DraftInput, body.Test)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, 201, expert.PublicEntry(e))
}
func (s *Server) listKnowledge(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.trustedActor(r); !ok {
		writeError(w, r, 403, "forbidden", "Operator access required")
		return
	}
	entries, err := s.service.ListKnowledge()
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]expert.KnowledgeEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, expert.PublicEntry(e))
	}
	writeJSON(w, 200, map[string]interface{}{"entries": out})
}
func (s *Server) getKnowledge(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.trustedActor(r); !ok {
		writeError(w, r, 403, "forbidden", "Operator access required")
		return
	}
	e, err := s.service.GetKnowledge(r.PathValue("knowledgeID"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, 200, expert.PublicEntry(e))
}
func readKnowledgeCommand(w http.ResponseWriter, r *http.Request) (expert.Command, map[string]interface{}, bool) {
	var body map[string]interface{}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
		writeError(w, r, 400, "invalid_json", "Invalid command")
		return expert.Command{}, nil, false
	}
	version, _ := body["expected_version"].(float64)
	id, _ := body["command_id"].(string)
	return expert.Command{ExpectedVersion: int(version), ID: id}, body, true
}
func (s *Server) approveKnowledge(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.trustedActor(r)
	if !ok {
		writeError(w, r, 403, "forbidden", "Operator access required")
		return
	}
	cmd, b, ok := readKnowledgeCommand(w, r)
	if !ok {
		return
	}
	revision, _ := b["revision"].(float64)
	raw, _ := json.Marshal(b["checklist"])
	var checklist expert.ReviewChecklist
	if err := json.Unmarshal(raw, &checklist); err != nil {
		writeError(w, r, 400, "invalid_checklist", "Invalid review checklist")
		return
	}
	e, err := s.service.ApproveKnowledge(r.PathValue("knowledgeID"), actor, cmd, int(revision), checklist)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, 200, expert.PublicEntry(e))
}
func (s *Server) revokeKnowledge(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.trustedActor(r)
	if !ok {
		writeError(w, r, 403, "forbidden", "Operator access required")
		return
	}
	cmd, b, ok := readKnowledgeCommand(w, r)
	if !ok {
		return
	}
	reason, _ := b["reason"].(string)
	e, err := s.service.RevokeKnowledge(r.PathValue("knowledgeID"), actor, cmd, reason)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, 200, expert.PublicEntry(e))
}
func (s *Server) assignExpert(w http.ResponseWriter, r *http.Request) {
	s.operatorCommand(w, r, func(a expert.Actor, c expert.Command, b map[string]interface{}) (expert.Request, error) {
		x, _ := b["assignee_id"].(string)
		return s.service.AssignExpert(r.PathValue("requestID"), a, c, x)
	})
}
func (s *Server) startExpert(w http.ResponseWriter, r *http.Request) {
	s.operatorCommand(w, r, func(a expert.Actor, c expert.Command, _ map[string]interface{}) (expert.Request, error) {
		return s.service.StartExpert(r.PathValue("requestID"), a, c)
	})
}
func (s *Server) replyExpert(w http.ResponseWriter, r *http.Request) {
	s.operatorCommand(w, r, func(a expert.Actor, c expert.Command, b map[string]interface{}) (expert.Request, error) {
		id, _ := b["reply_id"].(string)
		body, _ := b["body"].(string)
		return s.service.AnswerExpert(r.PathValue("requestID"), a, c, id, body)
	})
}
func (s *Server) cancelExpert(w http.ResponseWriter, r *http.Request) {
	s.operatorCommand(w, r, func(a expert.Actor, c expert.Command, b map[string]interface{}) (expert.Request, error) {
		reason, _ := b["reason"].(string)
		comment, _ := b["comment"].(string)
		return s.service.CancelExpert(r.PathValue("requestID"), a, c, expert.CancelReason(reason), comment)
	})
}
