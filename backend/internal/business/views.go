package business

import (
	"time"
	"unicode/utf8"
)

var statusLabels = map[ReviewStatus]string{
	StatusNew: "новая", StatusInReview: "в работе", StatusNeedsInfo: "нужны данные",
	StatusAccepted: "принята", StatusRejected: "отклонена",
}

// ListItem is the queue row. It carries no phone: contacts are shown only in the card.
type ListItem struct {
	ID            string       `json:"id"`
	Number        string       `json:"number,omitempty"`
	SessionID     string       `json:"session_id"`
	Version       int          `json:"version"`
	Status        ReviewStatus `json:"status"`
	StatusLabel   string       `json:"status_label"`
	Reason        ReasonCode   `json:"reason,omitempty"`
	Demo          bool         `json:"demo"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
	ContactName   *string      `json:"contact_name"`
	Company       *string      `json:"company"`
	Task          string       `json:"task"`
	Products      []string     `json:"products"`
	UnknownsCount int          `json:"unknowns_count"`
	NextStep      string       `json:"next_step"`
}

func optional(f Field) *string {
	if f.Status == Unknown || f.Value == "" {
		return nil
	}
	v := f.Value
	return &v
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func NewListItem(rec ApplicationRecord) ListItem {
	item := ListItem{ID: rec.ID, Number: rec.Card.ApplicationNumber, SessionID: rec.SessionID, Version: rec.Version, Status: rec.Status,
		StatusLabel: statusLabels[rec.Status], Reason: rec.Reason, Demo: rec.Demo,
		CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt,
		ContactName: optional(rec.Card.Contact.FullName), Company: optional(rec.Card.Contact.Company),
		Task: truncate(rec.Card.Task.Value, 200), Products: []string{},
		UnknownsCount: len(rec.Card.Unknowns), NextStep: rec.Card.NextStep.Value}
	for _, p := range rec.Card.Products {
		item.Products = append(item.Products, p.Name)
	}
	return item
}

// PublicView is the record as returned by the API: without internal request-id bookkeeping.
func PublicView(rec ApplicationRecord) ApplicationRecord {
	out := clone(rec)
	out.RequestIDs = nil
	out.RequestHashes = nil
	return out
}

// StatusLabel returns the Russian UI label of a status.
func StatusLabel(s ReviewStatus) string { return statusLabels[s] }
