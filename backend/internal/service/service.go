package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"energytool/backend/internal/consultation"
	"energytool/backend/internal/domain"
	"energytool/backend/internal/expert"
	"energytool/backend/internal/store"
)

var (
	ErrInvalidApplication = errors.New("invalid application")
	ErrNotFound           = store.ErrNotFound
	ErrClosed             = errors.New("session is closed")
	ErrHistoryLimit       = errors.New("session history exceeds supported limit")
	ErrMessageTooLong     = errors.New("message exceeds 10000 characters")
	ErrEmptyMessage       = errors.New("message must not be empty")
	ErrRequestConflict    = errors.New("client request id was already used with different data")
	ErrRequestNotFound    = errors.New("client request id not found")
	ErrRequestPending     = errors.New("request is still being processed")
	ErrSessionConflict    = errors.New("session changed while processing")
	errRequestInterrupted = errors.New("request was interrupted before its result was saved")
)

type Service struct {
	store        store.Store
	ml           consultation.Client
	expertStore  store.ExpertStore
	snapshotPath string
	snapshotMu   sync.Mutex
	demo         bool
	locks        sync.Map
	messageLocks sync.Map
	inflight     sync.Map
}

func New(s store.Store, ml consultation.Client) *Service {
	es, _ := s.(store.ExpertStore)
	return &Service{store: s, ml: ml, expertStore: es}
}

func (s *Service) SetSnapshotPath(path string) { s.snapshotPath = path }
func (s *Service) SetDemoMode(demo bool)       { s.demo = demo }

func (s *Service) CreateSession() (domain.Session, error) {
	now := time.Now().UTC()
	id := newID()
	session := domain.Session{Demo: s.demo, ID: id, Status: "active", CreatedAt: now, UpdatedAt: now,
		Messages: []domain.Message{}, Lead: domain.LeadState{SessionID: id,
			Fields: map[string]string{}, Evidence: map[string]interface{}{}}, Requests: map[string]domain.RequestRecord{}, ApplicationRequests: map[string]domain.ApplicationRequestRecord{}}
	if err := s.store.Create(session); err != nil {
		return domain.Session{}, err
	}
	return session, nil
}

func (s *Service) GetSession(id string) (domain.Session, error) {
	unlock := s.lock(id)
	defer unlock()
	session, err := s.store.Get(id)
	if err != nil {
		return domain.Session{}, err
	}
	return s.recoverInterrupted(session)
}
func (s *Service) ListSessions() ([]domain.Session, error) { return s.store.List() }

func (s *Service) ResumeSession(id string) (domain.Session, error) {
	unlock := s.lock(id)
	defer unlock()
	session, err := s.store.Get(id)
	if err != nil {
		return domain.Session{}, err
	}
	session, err = s.recoverInterrupted(session)
	if err != nil {
		return domain.Session{}, err
	}
	if session.Status != "active" {
		session.Status, session.ClosedAt, session.UpdatedAt = "active", nil, time.Now().UTC()
		if err := s.store.Save(session); err != nil {
			return domain.Session{}, err
		}
	}
	return session, nil
}

func (s *Service) CloseSession(id string) (domain.Session, error) {
	unlock := s.lock(id)
	defer unlock()
	session, err := s.store.Get(id)
	if err != nil {
		return domain.Session{}, err
	}
	if session.Status != "closed" {
		now := time.Now().UTC()
		session.Status, session.ClosedAt, session.UpdatedAt = "closed", &now, now
		if err := s.store.Save(session); err != nil {
			return domain.Session{}, err
		}
	}
	return session, nil
}

func (s *Service) Lead(id string) (domain.LeadState, error) {
	session, err := s.store.Get(id)
	return session.Lead, err
}

func (s *Service) SendMessage(ctx context.Context, id, content string) (domain.ConsultationResult, error) {
	return s.SendMessageWithID(ctx, id, "", content)
}

func (s *Service) SendMessageWithID(ctx context.Context, id, requestID, content string) (domain.ConsultationResult, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return domain.ConsultationResult{}, ErrEmptyMessage
	}
	if len([]rune(content)) > 10000 {
		return domain.ConsultationResult{}, ErrMessageTooLong
	}
	finishMessage, err := s.lockMessage(ctx, id)
	if err != nil {
		return domain.ConsultationResult{}, err
	}
	defer finishMessage()
	unlock := s.lock(id)
	defer func() { unlock() }()
	session, err := s.store.Get(id)
	if err != nil {
		return domain.ConsultationResult{}, err
	}
	if session.Status != "active" {
		return domain.ConsultationResult{}, ErrClosed
	}
	session, err = s.recoverInterrupted(session)
	if err != nil {
		return domain.ConsultationResult{}, err
	}
	if session.Requests == nil {
		session.Requests = map[string]domain.RequestRecord{}
	}
	fingerprint := requestFingerprint("message", content)
	if requestID != "" {
		if record, ok := session.Requests[requestID]; ok {
			if record.Fingerprint != fingerprint {
				return domain.ConsultationResult{}, ErrRequestConflict
			}
			record.Result.RequestID = requestID
			return record.Result, nil
		}
	}
	if len(session.Messages)+1 > 200 {
		return domain.ConsultationResult{}, ErrHistoryLimit
	}
	totalChars := len([]rune(content))
	for _, message := range session.Messages {
		totalChars += len([]rune(message.Content))
	}
	if totalChars > 100000 {
		return domain.ConsultationResult{}, ErrHistoryLimit
	}

	userMessage := domain.Message{ID: newID(), Role: "user", Content: content,
		Sequence: len(session.Messages) + 1, CreatedAt: time.Now().UTC()}
	session.Messages = append(session.Messages, userMessage)
	session.UpdatedAt = userMessage.CreatedAt
	if requestID != "" {
		session.Requests[requestID] = domain.RequestRecord{Fingerprint: fingerprint, Pending: true, MessageID: userMessage.ID}
	}
	if err := s.store.Save(session); err != nil {
		return domain.ConsultationResult{}, err
	}
	if s.ml == nil {
		return s.recordTechnicalFailureWithID(session, requestID, fingerprint, errors.New("consultation service is not configured"), 0)
	}
	mlRequest := domain.ConsultationRequest{SessionID: session.ID, Messages: make([]domain.MessageInput, 0, len(session.Messages))}
	if session.LastResult != nil {
		lead := session.Lead
		mlRequest.Lead = &lead
		mlRequest.SummarySoFar = session.LastResult.Summary
	}
	for _, message := range session.Messages {
		mlRequest.Messages = append(mlRequest.Messages, domain.MessageInput{Role: message.Role, Content: message.Content})
	}
	// Only messages serialize behind generation. Contacts, navigation and expert
	// replies must remain available while the model is slow or disconnected.
	s.inflight.Store(id, true)
	defer s.inflight.Delete(id)
	unlock()
	unlock = func() {}
	mlStarted := time.Now()
	result, err := s.ml.Respond(ctx, mlRequest)
	unlock = s.lock(id)
	// A form or specialist reply may have been saved while the model ran.
	// Merge into that latest state instead of overwriting it with our snapshot.
	current, getErr := s.store.Get(id)
	if getErr != nil {
		return domain.ConsultationResult{}, getErr
	}
	session = current
	if err != nil {
		return s.recordTechnicalFailureWithID(session, requestID, fingerprint, err, time.Since(mlStarted))
	}
	if result.Debug == nil {
		result.Debug = map[string]interface{}{}
	}
	result.RequestID = requestID
	result.Debug["ml_elapsed_ms"] = result.ElapsedMS
	result.ElapsedMS = time.Since(mlStarted).Milliseconds()
	result.Debug["backend_elapsed_ms"] = result.ElapsedMS
	if result.Status == "technical_error" {
		// The v2 adapter builds an evidence-backed snapshot even if generation
		// fails. Keep its new user facts; legacy/empty errors retain prior state.
		result.Lead = session.Lead
		version := result.Summary["state_version"]
		if session.LastResult != nil && version != 2 && version != float64(2) {
			result.Summary = session.LastResult.Summary
		}
	}
	if result.Lead.SessionID != session.ID {
		return s.recordTechnicalFailureWithID(session, requestID, fingerprint, errors.New("ML returned mismatched session id"), time.Since(mlStarted))
	}
	assistant := domain.Message{ID: newID(), Role: "assistant", Content: result.Answer, Response: &domain.ResponseMeta{RequestID: requestID, Status: result.Status, Sources: result.Sources, ElapsedMS: result.ElapsedMS, Debug: result.Debug, Summary: result.Summary},
		Sequence: len(session.Messages) + 1, CreatedAt: time.Now().UTC()}
	session.Messages = append(session.Messages, assistant)
	session.UpdatedAt = assistant.CreatedAt
	session.Lead = result.Lead
	session.LastResult = &result
	if requestID != "" {
		session.Requests[requestID] = domain.RequestRecord{Fingerprint: fingerprint, Result: result}
	}
	if err := s.store.Save(session); err != nil {
		return domain.ConsultationResult{}, err
	}
	if result.Status == "knowledge_gap" {
		if session.PendingExpertGaps == nil {
			session.PendingExpertGaps = map[string]bool{}
		}
		session.PendingExpertGaps[userMessage.ID] = true
		if err := s.store.Save(session); err == nil {
			_ = s.registerKnowledgeGap(session, userMessage.ID, result)
		}
	}
	return result, nil
}

func (s *Service) registerKnowledgeGap(session domain.Session, originID string, result domain.ConsultationResult) error {
	if s.expertStore == nil {
		return errors.New("expert store is not configured")
	}
	if old, err := s.expertStore.GetExpertRequestByKey(session.ID, originID); err != nil {
		return err
	} else if old != nil {
		return nil
	}
	messages := make([]expert.ClientMessage, 0, len(session.Messages))
	for _, m := range session.Messages {
		messages = append(messages, expert.ClientMessage{ID: m.ID, Role: m.Role, Content: m.Content, Sequence: m.Sequence})
	}
	r, created, err := expert.RegisterGap(nil, expert.GapSignal{SessionID: session.ID, OriginMessageID: originID, ResultStatus: result.Status, Demo: session.Demo, At: time.Now().UTC()}, messages, newID(), time.Now().UTC())
	if err != nil || !created {
		return err
	}
	return s.expertStore.SaveExpertRequest(r, 0)
}

func (s *Service) RetryPendingExpertGaps() error {
	sessions, err := s.store.List()
	if err != nil {
		return err
	}
	for _, session := range sessions {
		for originID := range session.PendingExpertGaps {
			// The durable pending entry belongs to an earlier gap; a newer answer
			// must not prevent its registration after a transient store failure.
			if err := s.registerKnowledgeGap(session, originID, domain.ConsultationResult{Status: "knowledge_gap"}); err != nil {
				continue
			}
			unlock := s.lock(session.ID)
			current, getErr := s.store.Get(session.ID)
			if getErr == nil {
				delete(current.PendingExpertGaps, originID)
				_ = s.store.Save(current)
			}
			unlock()
		}
	}
	return nil
}

func (s *Service) ListExpertRequests(f expert.RequestFilter) ([]expert.Request, error) {
	if s.expertStore == nil {
		return nil, errors.New("expert store is not configured")
	}
	return s.expertStore.ListExpertRequests(f)
}
func (s *Service) GetExpertRequest(id string) (expert.Request, error) {
	if s.expertStore == nil {
		return expert.Request{}, errors.New("expert store is not configured")
	}
	return s.expertStore.GetExpertRequest(id)
}
func (s *Service) mutateExpert(id string, actor expert.Actor, cmd expert.Command, mutate func(expert.Request) (expert.Request, error)) (expert.Request, error) {
	if s.expertStore == nil {
		return expert.Request{}, errors.New("expert store is not configured")
	}
	r, err := s.expertStore.GetExpertRequest(id)
	if err != nil {
		return r, err
	}
	next, err := mutate(r)
	if err != nil {
		return r, err
	}
	if err := s.expertStore.SaveExpertRequest(next, r.Version); err != nil {
		return r, err
	}
	return next, nil
}
func (s *Service) AssignExpert(id string, actor expert.Actor, cmd expert.Command, assignee string) (expert.Request, error) {
	return s.mutateExpert(id, actor, cmd, func(r expert.Request) (expert.Request, error) {
		return expert.Assign(r, actor, cmd, assignee, time.Now())
	})
}
func (s *Service) StartExpert(id string, actor expert.Actor, cmd expert.Command) (expert.Request, error) {
	return s.mutateExpert(id, actor, cmd, func(r expert.Request) (expert.Request, error) { return expert.Take(r, actor, cmd, time.Now()) })
}
func (s *Service) CancelExpert(id string, actor expert.Actor, cmd expert.Command, reason expert.CancelReason, comment string) (expert.Request, error) {
	return s.mutateExpert(id, actor, cmd, func(r expert.Request) (expert.Request, error) {
		return expert.Cancel(r, actor, cmd, reason, comment, time.Now())
	})
}

func (s *Service) ListKnowledge() ([]expert.KnowledgeEntry, error) {
	if s.expertStore == nil {
		return nil, errors.New("expert store is not configured")
	}
	return s.expertStore.ListKnowledge()
}
func (s *Service) GetKnowledge(id string) (expert.KnowledgeEntry, error) {
	if s.expertStore == nil {
		return expert.KnowledgeEntry{}, errors.New("expert store is not configured")
	}
	return s.expertStore.GetKnowledge(id)
}
func (s *Service) CreateKnowledge(actor expert.Actor, sourceID string, input expert.DraftInput, test bool) (expert.KnowledgeEntry, error) {
	var source *expert.Request
	if sourceID != "" {
		r, err := s.GetExpertRequest(sourceID)
		if err != nil {
			return expert.KnowledgeEntry{}, err
		}
		source = &r
	}
	e, err := expert.NewDraft(source, input, actor, newID(), test, time.Now())
	if err != nil {
		return e, err
	}
	if err := s.expertStore.SaveKnowledge(e, 0); err != nil {
		return e, err
	}
	return e, nil
}
func (s *Service) ApproveKnowledge(id string, actor expert.Actor, cmd expert.Command, revision int, checklist expert.ReviewChecklist) (expert.KnowledgeEntry, error) {
	e, err := s.GetKnowledge(id)
	if err != nil {
		return e, err
	}
	next, err := expert.Approve(e, actor, cmd, revision, checklist, time.Now())
	if err != nil {
		return e, err
	}
	if err := s.expertStore.SaveKnowledge(next, e.Version); err != nil {
		return e, err
	}
	if err := s.writeSnapshot(); err != nil {
		return next, err
	}
	return s.GetKnowledge(id)
}
func (s *Service) RevokeKnowledge(id string, actor expert.Actor, cmd expert.Command, reason string) (expert.KnowledgeEntry, error) {
	e, err := s.GetKnowledge(id)
	if err != nil {
		return e, err
	}
	next, err := expert.Revoke(e, actor, cmd, reason, time.Now())
	if err != nil {
		return e, err
	}
	if err := s.expertStore.SaveKnowledge(next, e.Version); err != nil {
		return e, err
	}
	if err := s.writeSnapshot(); err != nil {
		return next, err
	}
	return s.GetKnowledge(id)
}
func (s *Service) writeSnapshot() error {
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	if s.snapshotPath == "" {
		return nil
	}
	entries, err := s.expertStore.ListKnowledge()
	if err != nil {
		return err
	}
	kbVersion := fmt.Sprintf("web-%d", time.Now().UTC().UnixNano())
	payload := struct {
		KBVersion string              `json:"kb_version"`
		Documents []expert.KBDocument `json:"documents"`
	}{kbVersion, expert.ActiveSet(entries, false)}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.snapshotPath), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.snapshotPath), ".expert-kb-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, s.snapshotPath); err != nil {
		return err
	}
	included := map[string]int{}
	for _, entry := range entries {
		if rev := entry.InForce(); rev != nil {
			included[entry.ID] = rev.Revision
		}
	}
	for _, entry := range entries {
		result := expert.ActivationResult{KBVersion: kbVersion, OK: true, Included: included}
		updated, err := expert.ApplyActivation(entry, expert.Actor{ID: "web-backend", Role: expert.RoleSystem}, expert.Command{ExpectedVersion: entry.Version, ID: "activate-" + kbVersion}, result, time.Now())
		if err != nil {
			continue
		}
		_ = s.expertStore.SaveKnowledge(updated, entry.Version)
	}
	return nil
}
func (s *Service) AnswerExpert(id string, actor expert.Actor, cmd expert.Command, replyID, body string) (expert.Request, error) {
	r, err := s.mutateExpert(id, actor, cmd, func(r expert.Request) (expert.Request, error) {
		return expert.Answer(r, actor, cmd, replyID, body, time.Now())
	})
	if err != nil {
		return r, err
	}
	return s.deliverExpert(r, replyID)
}
func (s *Service) deliverExpert(r expert.Request, replyID string) (expert.Request, error) {
	unlock := s.lock(r.SessionID)
	defer unlock()
	session, err := s.store.Get(r.SessionID)
	if err != nil {
		return r, err
	}
	if r.Reply == nil {
		return r, errors.New("expert reply is missing")
	}
	if r.Reply.Delivery == expert.DeliveryDelivered {
		return r, nil
	}
	return s.expertStore.DeliverExpertReply(r, replyID, session.ID)
}

func (s *Service) recordTechnicalFailureWithID(session domain.Session, requestID, fingerprint string, cause error, elapsed time.Duration) (domain.ConsultationResult, error) {
	answer := "Сейчас не удалось получить ответ из-за технического сбоя. История диалога сохранена; попробуйте позже."
	result := domain.ConsultationResult{Answer: answer, Status: "technical_error", Sources: []map[string]interface{}{},
		ElapsedMS: elapsed.Milliseconds(), Lead: session.Lead, LeadReady: false, Summary: map[string]interface{}{},
		Debug: map[string]interface{}{"error_code": "consultation_unavailable"}, SchemaVersion: "1.0"}
	result.RequestID = requestID
	if errors.Is(cause, errRequestInterrupted) {
		answer = "Подготовка ответа была прервана. Ваш вопрос и история сохранены; можно повторить запрос или обратиться к оператору."
		result.Answer = answer
		result.Debug["error_code"] = "request_interrupted"
	}
	if session.LastResult != nil {
		result.Summary = session.LastResult.Summary
		result.LeadReady = session.LastResult.LeadReady
	}
	result.Debug["backend_elapsed_ms"] = result.ElapsedMS
	assistant := domain.Message{ID: newID(), Role: "assistant", Content: answer, Response: &domain.ResponseMeta{RequestID: requestID, Status: result.Status, Sources: result.Sources, ElapsedMS: result.ElapsedMS, Debug: result.Debug, Summary: result.Summary}, Sequence: len(session.Messages) + 1, CreatedAt: time.Now().UTC()}
	session.Messages = append(session.Messages, assistant)
	session.UpdatedAt = assistant.CreatedAt
	session.LastResult = &result
	if requestID != "" {
		if session.Requests == nil {
			session.Requests = map[string]domain.RequestRecord{}
		}
		session.Requests[requestID] = domain.RequestRecord{Fingerprint: fingerprint, Result: result}
	}
	// Avoid returning or logging the raw upstream error, which could contain private details.
	_ = cause
	if err := s.store.Save(session); err != nil {
		return domain.ConsultationResult{}, fmt.Errorf("save technical fallback result: %w", err)
	}
	return result, nil
}

func (s *Service) lock(id string) func() {
	value, _ := s.locks.LoadOrStore(id, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

func (s *Service) lockMessage(ctx context.Context, id string) (func(), error) {
	value, _ := s.messageLocks.LoadOrStore(id, make(chan struct{}, 1))
	gate := value.(chan struct{})
	select {
	case gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-gate
			return nil, err
		}
		return func() { <-gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Called with the session lock held. A durable user turn without a live
// generator can occur after a process restart; settle it once so the client
// can retry instead of polling forever. Also recover pre-request-ID sessions.
func (s *Service) recoverInterrupted(session domain.Session) (domain.Session, error) {
	if _, running := s.inflight.Load(session.ID); running {
		return session, nil
	}
	for id, record := range session.Requests {
		if !record.Pending {
			continue
		}
		if _, err := s.recordTechnicalFailureWithID(session, id, record.Fingerprint, errRequestInterrupted, 0); err != nil {
			return domain.Session{}, err
		}
		var err error
		session, err = s.store.Get(session.ID)
		if err != nil {
			return domain.Session{}, err
		}
	}
	if len(session.Messages) > 0 && session.Messages[len(session.Messages)-1].Role == "user" {
		if _, err := s.recordTechnicalFailureWithID(session, "", "", errRequestInterrupted, 0); err != nil {
			return domain.Session{}, err
		}
		return s.store.Get(session.ID)
	}
	return session, nil
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d-%d", time.Now().UnixNano(), time.Now().Unix())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// SaveApplication stores the user's reviewed form, independently of ML extraction.
// It is a local handoff artifact, not a delivery acknowledgement from a CRM.
func (s *Service) SaveApplication(id string, input domain.Application) (domain.Session, error) {
	return s.SaveApplicationWithID(id, "", input)
}

func (s *Service) SaveApplicationWithID(id, requestID string, input domain.Application) (domain.Session, error) {
	unlock := s.lock(id)
	defer unlock()
	session, err := s.store.Get(id)
	if err != nil {
		return domain.Session{}, err
	}
	if session.Status != "active" {
		return domain.Session{}, ErrClosed
	}
	if session.ApplicationRequests == nil {
		session.ApplicationRequests = map[string]domain.ApplicationRequestRecord{}
	}
	input.RequestID = requestID
	fingerprint := requestFingerprint("application", input.FullName, input.Phone, input.Email, input.Company, input.Summary, input.CallbackMode, input.CallbackAt)
	if requestID != "" {
		if record, ok := session.ApplicationRequests[requestID]; ok {
			if record.Fingerprint != fingerprint {
				return domain.Session{}, ErrRequestConflict
			}
			session.Application = &record.Application
			return session, nil
		}
	}
	input.FullName = strings.TrimSpace(input.FullName)
	input.Phone = strings.TrimSpace(input.Phone)
	input.Email = strings.TrimSpace(input.Email)
	input.Company = strings.TrimSpace(input.Company)
	input.Summary = strings.TrimSpace(input.Summary)
	if !domain.ValidContacts(input.Phone, input.Email) || input.Summary == "" || len([]rune(input.Summary)) > 2000 || len([]rune(input.FullName)) > 200 || len([]rune(input.Company)) > 200 {
		return domain.Session{}, ErrInvalidApplication
	}

	switch input.CallbackMode {
	case "", "asap":
		if input.CallbackAt != "" {
			return domain.Session{}, ErrInvalidApplication
		}
	case "scheduled":
		at, parseErr := time.Parse(time.RFC3339, input.CallbackAt)
		if parseErr != nil {
			return domain.Session{}, ErrInvalidApplication
		}
		input.CallbackAt = at.In(time.FixedZone("MSK", 3*3600)).Format(time.RFC3339)
		unchanged := session.Application != nil && session.Application.CallbackMode == "scheduled" && session.Application.CallbackAt == input.CallbackAt
		if !unchanged && !at.After(time.Now()) {
			return domain.Session{}, ErrInvalidApplication
		}
	default:
		return domain.Session{}, ErrInvalidApplication
	}
	input.ID = domain.ApplicationID(session.ID)
	if session.Application != nil && session.Application.ID != "" {
		input.ID = session.Application.ID
	}
	input.SavedAt = time.Now().UTC()
	input.RequestID = requestID
	session.Application = &input
	if requestID != "" {
		session.ApplicationRequests[requestID] = domain.ApplicationRequestRecord{Fingerprint: fingerprint, Application: input}
	}
	session.UpdatedAt = input.SavedAt
	if err := s.store.Save(session); err != nil {
		return domain.Session{}, err
	}
	return session, nil
}

func (s *Service) FindRequest(id, requestID string) (domain.ConsultationResult, error) {
	session, err := s.GetSession(id)
	if err != nil {
		return domain.ConsultationResult{}, err
	}
	record, ok := session.Requests[requestID]
	if !ok {
		return domain.ConsultationResult{}, ErrRequestNotFound
	}
	if record.Pending {
		return domain.ConsultationResult{}, ErrRequestPending
	}
	return record.Result, nil
}

func requestFingerprint(kind string, values ...string) string {
	h := sha256.New()
	h.Write([]byte(kind))
	for _, value := range values {
		h.Write([]byte{0})
		h.Write([]byte(value))
	}
	return hex.EncodeToString(h.Sum(nil))
}
