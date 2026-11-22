package expert

import (
	"encoding/json"
	"fmt"
	"time"
)

type RequestStatus string

const (
	StatusQueued    RequestStatus = "queued"
	StatusInReview  RequestStatus = "in_review"
	StatusAnswered  RequestStatus = "answered"
	StatusCancelled RequestStatus = "cancelled"
)

type DeliveryStatus string

const (
	DeliveryPending   DeliveryStatus = "pending"
	DeliveryDelivered DeliveryStatus = "delivered" // available in the stored session history, not "read"
	DeliveryFailed    DeliveryStatus = "failed"
)

type CancelReason string

var cancelReasons = map[CancelReason]string{
	"duplicate":          "дубль обращения",
	"not_relevant":       "вопрос не относится к продуктам Energytool",
	"resolved_elsewhere": "вопрос решён другим способом",
	"other":              "другое",
}

// CancelReasons returns allowed cancel reasons with Russian labels (for UI).
func CancelReasons() map[CancelReason]string {
	out := map[CancelReason]string{}
	for k, v := range cancelReasons {
		out[k] = v
	}
	return out
}

// ClientMessage is the input DTO of a stored session message (filled by the core API).
type ClientMessage struct {
	ID       string `json:"id"`
	Role     string `json:"role"` // user | assistant
	Content  string `json:"content"`
	Sequence int    `json:"sequence"`
}

// GapSignal is the structured consultant result for one stored client message.
type GapSignal struct {
	SessionID       string    `json:"session_id"`
	OriginMessageID string    `json:"origin_message_id"`
	ResultStatus    string    `json:"result_status"`
	ApplicationID   string    `json:"application_id,omitempty"`
	Demo            bool      `json:"demo"`
	At              time.Time `json:"at"`
}

type ContextItem struct {
	MessageID string `json:"message_id"`
	Sequence  int    `json:"sequence"`
	Text      string `json:"text"`
}

type Reply struct {
	ID               string         `json:"id"`
	RequestID        string         `json:"request_id"`
	AuthorID         string         `json:"author_id"`
	Body             string         `json:"body"`  // exact final text shown to the client
	Label            string         `json:"label"` // «Ответ специалиста»
	CreatedAt        time.Time      `json:"created_at"`
	Version          int            `json:"version"` // request version that stored the reply
	Delivery         DeliveryStatus `json:"delivery_status"`
	DeliveredAt      *time.Time     `json:"delivered_at,omitempty"`
	DeliveryAttempts int            `json:"delivery_attempts"`
	DeliveryError    string         `json:"delivery_error,omitempty"` // error code only
	ToClosedSession  bool           `json:"to_closed_session,omitempty"`
}

type HistoryEntry struct {
	Version    int           `json:"version"`
	Action     string        `json:"action"`
	ActorID    string        `json:"actor_id"`
	Role       Role          `json:"role"`
	From       RequestStatus `json:"from,omitempty"`
	To         RequestStatus `json:"to,omitempty"`
	AssigneeID string        `json:"assignee_id,omitempty"`
	Reason     CancelReason  `json:"reason,omitempty"`
	At         time.Time     `json:"at"`
}

// Request is one stored specialist request. Key: SessionID + OriginMessageID.
type Request struct {
	ID              string         `json:"id"`
	SessionID       string         `json:"session_id"`
	OriginMessageID string         `json:"origin_message_id"`
	ApplicationID   string         `json:"application_id,omitempty"`
	Question        string         `json:"question"`
	Context         []ContextItem  `json:"necessary_context"`
	AssigneeID      string         `json:"assignee_id,omitempty"`
	Status          RequestStatus  `json:"status"`
	CancelReason    CancelReason   `json:"cancel_reason,omitempty"`
	CancelComment   string         `json:"cancel_comment,omitempty"`
	Version         int            `json:"version"`
	Demo            bool           `json:"demo"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	Reply           *Reply         `json:"reply,omitempty"`
	History         []HistoryEntry `json:"history"`
	Commands        replayLog      `json:"commands"`
}

// RequestKey is the uniqueness key the store must enforce (lock + lookup) before RegisterGap.
func RequestKey(sessionID, originMessageID string) string {
	key, _ := json.Marshal([2]string{sessionID, originMessageID})
	return string(key)
}

const (
	maxQuestion     = 4000
	maxContextItems = 3
	maxContextText  = 500
	maxReply        = 4000
)

// RegisterGap creates a request from a knowledge_gap signal. It returns created=false and no
// error when the signal must not create a request (other statuses) or when existing is the
// request already stored for the same key (replay). messages are the session's stored messages.
func RegisterGap(existing *Request, signal GapSignal, messages []ClientMessage, newID string, now time.Time) (Request, bool, error) {
	if signal.ResultStatus != "knowledge_gap" {
		return Request{}, false, nil
	}
	if existing != nil {
		if existing.SessionID != signal.SessionID || existing.OriginMessageID != signal.OriginMessageID {
			return Request{}, false, fmt.Errorf("%w: existing request has another key", ErrInvalidInput)
		}
		return *existing, false, nil
	}
	if signal.SessionID == "" || signal.OriginMessageID == "" || newID == "" {
		return Request{}, false, fmt.Errorf("%w: session, origin message and id are required", ErrInvalidInput)
	}
	origin := -1
	for i, m := range messages {
		if m.ID == signal.OriginMessageID {
			origin = i
			break
		}
	}
	if origin < 0 || messages[origin].Role != "user" {
		return Request{}, false, fmt.Errorf("%w: origin must be a stored client message", ErrInvalidInput)
	}
	question, err := cleanText(truncate(messages[origin].Content, maxQuestion), maxQuestion)
	if err != nil {
		return Request{}, false, err
	}
	// Necessary context: up to 3 previous client messages, oldest first; no assistant text.
	context := []ContextItem{}
	for i := origin - 1; i >= 0 && len(context) < maxContextItems; i-- {
		if m := messages[i]; m.Role == "user" {
			context = append([]ContextItem{{MessageID: m.ID, Sequence: m.Sequence,
				Text: truncate(m.Content, maxContextText)}}, context...)
		}
	}
	at := utc(signal.At)
	if signal.At.IsZero() {
		at = utc(now)
	}
	r := Request{ID: newID, SessionID: signal.SessionID, OriginMessageID: signal.OriginMessageID,
		ApplicationID: signal.ApplicationID, Question: question, Context: context, Status: StatusQueued,
		Version: 1, Demo: signal.Demo, CreatedAt: at, UpdatedAt: at,
		History: []HistoryEntry{{Version: 1, Action: "register", ActorID: "system", Role: RoleSystem,
			To: StatusQueued, At: at}}}
	return r, true, nil
}

func cloneRequest(r Request) Request {
	out := r
	out.Context = append([]ContextItem{}, r.Context...)
	out.History = append([]HistoryEntry{}, r.History...)
	out.Commands = r.Commands.clone()
	if r.Reply != nil {
		reply := *r.Reply
		if r.Reply.DeliveredAt != nil {
			at := *r.Reply.DeliveredAt
			reply.DeliveredAt = &at
		}
		out.Reply = &reply
	}
	return out
}

// apply runs the shared command protocol: replay check, version check, mutation, history.
func apply(r Request, actor Actor, cmd Command, hash string, now time.Time,
	mutate func(next *Request) (HistoryEntry, error)) (Request, error) {
	if replay, err := checkCommand(r.Commands, r.Version, actor, cmd, hash); replay || err != nil {
		return r, err
	}
	next := cloneRequest(r)
	entry, err := mutate(&next)
	if err != nil {
		return r, err
	}
	next.Version++
	next.UpdatedAt = utc(now)
	entry.Version, entry.ActorID, entry.Role, entry.At = next.Version, actor.ID, actor.Role, utc(now)
	next.History = append(next.History, entry)
	next.Commands.remember(cmd.ID, hash, next.Version)
	return next, nil
}

// Assign sets or changes the specialist. Reassignment of an in-review request returns it to
// the queue so that the new specialist takes it explicitly.
func Assign(r Request, actor Actor, cmd Command, assigneeID string, now time.Time) (Request, error) {
	return apply(r, actor, cmd, fingerprint("assign", cmd.ExpectedVersion, actor, assigneeID), now,
		func(n *Request) (HistoryEntry, error) {
			if actor.Role != RoleCoordinator {
				return HistoryEntry{}, ErrForbidden
			}
			if assigneeID == "" {
				return HistoryEntry{}, fmt.Errorf("%w: assignee is required", ErrInvalidInput)
			}
			if n.Status != StatusQueued && n.Status != StatusInReview {
				return HistoryEntry{}, ErrInvalidTransition
			}
			if n.AssigneeID == assigneeID {
				return HistoryEntry{}, fmt.Errorf("%w: already assigned to this specialist", ErrInvalidTransition)
			}
			from := n.Status
			n.AssigneeID, n.Status = assigneeID, StatusQueued
			return HistoryEntry{Action: "assign", From: from, To: StatusQueued, AssigneeID: assigneeID}, nil
		})
}

// Take: the assigned specialist starts working on the request.
func Take(r Request, actor Actor, cmd Command, now time.Time) (Request, error) {
	return apply(r, actor, cmd, fingerprint("take", cmd.ExpectedVersion, actor), now,
		func(n *Request) (HistoryEntry, error) {
			if actor.Role != RoleSpecialist || actor.ID != n.AssigneeID {
				return HistoryEntry{}, ErrForbidden
			}
			if n.Status != StatusQueued {
				return HistoryEntry{}, ErrInvalidTransition
			}
			n.Status = StatusInReview
			return HistoryEntry{Action: "take", From: StatusQueued, To: StatusInReview}, nil
		})
}

// Answer stores the single final reply and the delivery intent (pending) in the same record.
func Answer(r Request, actor Actor, cmd Command, replyID, body string, now time.Time) (Request, error) {
	return apply(r, actor, cmd, fingerprint("answer", cmd.ExpectedVersion, actor, replyID, body), now,
		func(n *Request) (HistoryEntry, error) {
			if actor.Role != RoleSpecialist || actor.ID != n.AssigneeID {
				return HistoryEntry{}, ErrForbidden
			}
			if n.Status != StatusInReview || n.Reply != nil {
				return HistoryEntry{}, ErrInvalidTransition
			}
			text, err := cleanText(body, maxReply)
			if err != nil {
				return HistoryEntry{}, err
			}
			if replyID == "" {
				return HistoryEntry{}, fmt.Errorf("%w: reply id is required", ErrInvalidInput)
			}
			n.Status = StatusAnswered
			n.Reply = &Reply{ID: replyID, RequestID: n.ID, AuthorID: actor.ID, Body: text, Label: "Ответ специалиста",
				CreatedAt: utc(now), Version: n.Version + 1, Delivery: DeliveryPending}
			return HistoryEntry{Action: "answer", From: StatusInReview, To: StatusAnswered}, nil
		})
}

// Cancel closes an unanswered request with a reason (coordinator or the assignee).
func Cancel(r Request, actor Actor, cmd Command, reason CancelReason, comment string, now time.Time) (Request, error) {
	return apply(r, actor, cmd, fingerprint("cancel", cmd.ExpectedVersion, actor, reason, comment), now,
		func(n *Request) (HistoryEntry, error) {
			if actor.Role != RoleCoordinator && !(actor.Role == RoleSpecialist && actor.ID == n.AssigneeID) {
				return HistoryEntry{}, ErrForbidden
			}
			if n.Status != StatusQueued && n.Status != StatusInReview {
				return HistoryEntry{}, ErrInvalidTransition
			}
			if _, ok := cancelReasons[reason]; !ok {
				return HistoryEntry{}, fmt.Errorf("%w: unknown cancel reason", ErrInvalidInput)
			}
			from := n.Status
			n.Status, n.CancelReason, n.CancelComment = StatusCancelled, reason, truncate(comment, 1000)
			return HistoryEntry{Action: "cancel", From: from, To: StatusCancelled, Reason: reason}, nil
		})
}

// MarkDelivered: the core API added the reply to the session's stored history (system actor).
// toClosedSession records that the session was closed: the reply is linked in its history
// and the session is not reopened. Exact command retries are no-ops. A new command
// confirming the same delivery is recorded without another delivery attempt.
func MarkDelivered(r Request, actor Actor, cmd Command, replyID string, toClosedSession bool, now time.Time) (Request, error) {
	if actor.Role != RoleSystem {
		return r, ErrForbidden
	}
	hash := fingerprint("delivered", cmd.ExpectedVersion, actor, replyID, toClosedSession)
	if replay, err := checkCommand(r.Commands, r.Version, actor, cmd, hash); replay || err != nil {
		return r, err
	}
	if r.Reply != nil && r.Reply.ID == replyID && r.Reply.Delivery == DeliveryDelivered {
		if r.Reply.ToClosedSession != toClosedSession {
			return r, ErrInvalidTransition
		}
		return apply(r, actor, cmd, hash, now, func(n *Request) (HistoryEntry, error) {
			return HistoryEntry{Action: "delivery_confirmed"}, nil
		})
	}
	return apply(r, actor, cmd, hash, now,
		func(n *Request) (HistoryEntry, error) {
			if actor.Role != RoleSystem {
				return HistoryEntry{}, ErrForbidden
			}
			if n.Reply == nil || n.Reply.ID != replyID || n.Reply.Delivery != DeliveryPending {
				return HistoryEntry{}, ErrInvalidTransition
			}
			at := utc(now)
			n.Reply.Delivery, n.Reply.DeliveredAt, n.Reply.DeliveryError = DeliveryDelivered, &at, ""
			n.Reply.DeliveryAttempts++
			n.Reply.ToClosedSession = toClosedSession
			return HistoryEntry{Action: "delivered"}, nil
		})
}

// MarkDeliveryFailed records a failed attempt with an error code (no message text).
func MarkDeliveryFailed(r Request, actor Actor, cmd Command, replyID, errorCode string, now time.Time) (Request, error) {
	return apply(r, actor, cmd, fingerprint("delivery_failed", cmd.ExpectedVersion, actor, replyID, errorCode), now,
		func(n *Request) (HistoryEntry, error) {
			if actor.Role != RoleSystem {
				return HistoryEntry{}, ErrForbidden
			}
			if n.Reply == nil || n.Reply.ID != replyID || n.Reply.Delivery != DeliveryPending {
				return HistoryEntry{}, ErrInvalidTransition
			}
			n.Reply.Delivery, n.Reply.DeliveryError = DeliveryFailed, truncate(errorCode, 100)
			n.Reply.DeliveryAttempts++
			return HistoryEntry{Action: "delivery_failed"}, nil
		})
}

// RetryDelivery returns a failed delivery of the same reply to pending (system or coordinator).
func RetryDelivery(r Request, actor Actor, cmd Command, replyID string, now time.Time) (Request, error) {
	return apply(r, actor, cmd, fingerprint("retry_delivery", cmd.ExpectedVersion, actor, replyID), now,
		func(n *Request) (HistoryEntry, error) {
			if actor.Role != RoleSystem && actor.Role != RoleCoordinator {
				return HistoryEntry{}, ErrForbidden
			}
			if n.Reply == nil || n.Reply.ID != replyID || n.Reply.Delivery != DeliveryFailed {
				return HistoryEntry{}, ErrInvalidTransition
			}
			n.Reply.Delivery = DeliveryPending
			return HistoryEntry{Action: "retry_delivery"}, nil
		})
}

type ClientStatus struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}

// StatusForClient is the truthful status shown in the client's chat.
func StatusForClient(r Request) ClientStatus {
	switch {
	case r.Status == StatusQueued && r.AssigneeID == "":
		return ClientStatus{"awaiting_assignment", "Вопрос зарегистрирован, ожидает назначения специалиста"}
	case r.Status == StatusQueued:
		return ClientStatus{"assigned", "Вопрос передан специалисту"}
	case r.Status == StatusInReview:
		return ClientStatus{"in_review", "Специалист готовит ответ"}
	case r.Status == StatusAnswered && r.Reply != nil && r.Reply.Delivery == DeliveryDelivered:
		return ClientStatus{"answered", "Ответ специалиста"}
	case r.Status == StatusAnswered:
		return ClientStatus{"answer_pending", "Ответ специалиста готовится к выдаче"}
	default:
		return ClientStatus{"cancelled", "Обращение закрыто"}
	}
}

// PublicRequest hides command bookkeeping from API responses.
func PublicRequest(r Request) Request {
	out := cloneRequest(r)
	out.Commands = replayLog{}
	return out
}
