package domain

import "time"

type Application struct {
	RequestID    string    `json:"request_id,omitempty"`
	CallbackMode string    `json:"callback_mode,omitempty"`
	CallbackAt   string    `json:"callback_at,omitempty"`
	ID           string    `json:"id"`
	Number       string    `json:"number,omitempty"`
	FullName     string    `json:"full_name"`
	Phone        string    `json:"phone"`
	Email        string    `json:"email,omitempty"`
	Company      string    `json:"company"`
	Summary      string    `json:"summary"`
	SavedAt      time.Time `json:"saved_at"`
}

type Session struct {
	Demo                bool                                `json:"demo,omitempty"`
	Application         *Application                        `json:"application,omitempty"`
	ID                  string                              `json:"id"`
	Status              string                              `json:"status"`
	CreatedAt           time.Time                           `json:"created_at"`
	UpdatedAt           time.Time                           `json:"updated_at"`
	ClosedAt            *time.Time                          `json:"closed_at,omitempty"`
	Messages            []Message                           `json:"messages"`
	Lead                LeadState                           `json:"lead"`
	LastResult          *ConsultationResult                 `json:"last_result,omitempty"`
	Requests            map[string]RequestRecord            `json:"requests,omitempty"`
	ApplicationRequests map[string]ApplicationRequestRecord `json:"application_requests,omitempty"`
	PendingExpertGaps   map[string]bool                     `json:"pending_expert_gaps,omitempty"`
}

type RequestRecord struct {
	Fingerprint string             `json:"fingerprint"`
	Pending     bool               `json:"pending,omitempty"`
	MessageID   string             `json:"message_id,omitempty"`
	Result      ConsultationResult `json:"result"`
}

type ApplicationRequestRecord struct {
	Fingerprint string      `json:"fingerprint"`
	Application Application `json:"application"`
}

type ResponseMeta struct {
	Origin    string                   `json:"origin,omitempty"`
	ReplyID   string                   `json:"reply_id,omitempty"`
	RequestID string                   `json:"request_id,omitempty"`
	Status    string                   `json:"status"`
	Sources   []map[string]interface{} `json:"sources"`
	ElapsedMS int64                    `json:"elapsed_ms"`
	Debug     map[string]interface{}   `json:"debug,omitempty"`
	Summary   map[string]interface{}   `json:"summary,omitempty"`
}

type Message struct {
	Response  *ResponseMeta `json:"response,omitempty"`
	ID        string        `json:"id"`
	Role      string        `json:"role"`
	Content   string        `json:"content"`
	Sequence  int           `json:"sequence"`
	CreatedAt time.Time     `json:"created_at"`
}

type LeadState struct {
	SessionID string                 `json:"session_id"`
	Fields    map[string]string      `json:"fields"`
	Evidence  map[string]interface{} `json:"evidence"`
	Requested bool                   `json:"requested"`
	Declined  bool                   `json:"declined"`
}

type ConsultationRequest struct {
	SessionID    string                 `json:"session_id"`
	Messages     []MessageInput         `json:"messages"`
	Lead         *LeadState             `json:"lead"`
	SummarySoFar map[string]interface{} `json:"summary_so_far,omitempty"`
}

type MessageInput struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ConsultationResult struct {
	RequestID     string                   `json:"request_id,omitempty"`
	Answer        string                   `json:"answer"`
	Status        string                   `json:"status"`
	Sources       []map[string]interface{} `json:"sources"`
	ElapsedMS     int64                    `json:"elapsed_ms"`
	Lead          LeadState                `json:"lead"`
	LeadReady     bool                     `json:"lead_ready"`
	Summary       map[string]interface{}   `json:"summary"`
	Debug         map[string]interface{}   `json:"debug"`
	SchemaVersion string                   `json:"schema_version"`
}

type APIError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

// ApplicationID identifies the single reviewed application belonging to a session.
func ApplicationID(sessionID string) string { return "ET-" + sessionID }

// ContactPreference describes a requested callback, never a confirmed booking.
func (a Application) ContactPreference() string {
	if a.CallbackMode == "asap" {
		return "Как можно скорее (по возможности менеджера)"
	}
	if a.CallbackMode == "scheduled" {
		if at, err := time.Parse(time.RFC3339, a.CallbackAt); err == nil {
			return at.In(time.FixedZone("MSK", 3*3600)).Format("02.01.2006 в 15:04") + " МСК — желаемое время"
		}
	}
	return ""
}
