package business

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type EventType string

const (
	EventSessionStarted        EventType = "session_started"
	EventConsultationRequested EventType = "consultation_requested"
	EventFirstAnswerReceived   EventType = "first_answer_received"
	EventApplicationSaved      EventType = "application_saved"
	EventReviewStarted         EventType = "review_started"
	EventAccepted              EventType = "accepted"
	EventNeedsInfo             EventType = "needs_info"
	EventRejected              EventType = "rejected"
	// Optional extra (knowledge-gap journal): an answer with status knowledge_gap.
	EventKnowledgeGap EventType = "knowledge_gap"
)

// Event is a pilot analytics fact. It never contains names, phones or message text.
// The ID is deterministic, so replaying the same fact is deduplicated by the log.
type Event struct {
	ID            string     `json:"id"`
	Type          EventType  `json:"type"`
	SessionID     string     `json:"session_id"`
	ApplicationID string     `json:"application_id,omitempty"`
	Version       int        `json:"version,omitempty"`
	At            time.Time  `json:"at"`
	Demo          bool       `json:"demo"`
	Reason        ReasonCode `json:"reason,omitempty"`
	Topic         string     `json:"topic,omitempty"` // knowledge_gap only: KB document/product, not user text
}

// Session-side events: the core API calls these at the matching moments.
func SessionStarted(sessionID string, demo bool, at time.Time) Event {
	return Event{ID: "session_started:" + sessionID, Type: EventSessionStarted, SessionID: sessionID, At: at.UTC(), Demo: demo}
}

// ConsultationRequested is one per user message (messageID keeps retries idempotent).
func ConsultationRequested(sessionID, messageID string, demo bool, at time.Time) Event {
	return Event{ID: "consultation_requested:" + messageID, Type: EventConsultationRequested, SessionID: sessionID, At: at.UTC(), Demo: demo}
}

// FirstAnswerReceived is emitted for every non-technical answer; the deterministic ID keeps only the first.
func FirstAnswerReceived(sessionID string, demo bool, at time.Time) Event {
	return Event{ID: "first_answer_received:" + sessionID, Type: EventFirstAnswerReceived, SessionID: sessionID, At: at.UTC(), Demo: demo}
}

// KnowledgeGap records a knowledge_gap; other answer statuses produce no event.
// topic is a coarse label such as a knowledge-base document id; never the user's text.
func KnowledgeGap(sessionID, messageID, status, topic string, demo bool, at time.Time) Event {
	if status != "knowledge_gap" {
		return Event{}
	}
	if topic == "" {
		topic = "unknown"
	}
	return Event{ID: "knowledge_gap:" + messageID, Type: EventKnowledgeGap, SessionID: sessionID, At: at.UTC(),
		Demo: demo, Topic: status + ":" + topic}
}

func applicationEvent(t EventType, rec ApplicationRecord, at time.Time) Event {
	return Event{ID: fmt.Sprintf("%s:%s:v%d", t, rec.ID, rec.Version), Type: t, SessionID: rec.SessionID,
		ApplicationID: rec.ID, Version: rec.Version, At: at.UTC(), Demo: rec.Demo}
}

// EventLog is an append-only JSON-lines file, deduplicated by event ID.
// It is separate from the session store so analytics never touches contacts.
type EventLog struct {
	mu     sync.Mutex
	path   string
	seen   map[string]bool
	events []Event
}

func OpenEventLog(path string) (*EventLog, error) {
	l := &EventLog{path: path, seen: map[string]bool{}}
	if path == "" {
		return l, nil // in-memory (tests)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var e Event
		if json.Unmarshal(scanner.Bytes(), &e) != nil || e.ID == "" {
			continue // a torn last line after a crash is skipped, not fatal
		}
		if !l.seen[e.ID] {
			l.seen[e.ID] = true
			l.events = append(l.events, e)
		}
	}
	return l, scanner.Err()
}

// Append writes only events whose ID was not seen before; returns how many were new.
func (l *EventLog) Append(events ...Event) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fresh := []Event{}
	for _, e := range events {
		if e.ID == "" || l.seen[e.ID] {
			continue
		}
		fresh = append(fresh, e)
		l.seen[e.ID] = true
	}
	if len(fresh) == 0 {
		return 0, nil
	}
	if l.path != "" {
		f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o600)
		if err != nil {
			for _, e := range fresh {
				delete(l.seen, e.ID)
			}
			return 0, err
		}
		w := bufio.NewWriter(f)
		// Separate an unterminated last line without adding blank JSONL records.
		info, err := f.Stat()
		if err == nil && info.Size() > 0 {
			var last [1]byte
			_, err = f.ReadAt(last[:], info.Size()-1)
			if err == nil && last[0] != '\n' {
				err = w.WriteByte('\n')
			}
		}
		if err == nil {
			for _, e := range fresh {
				raw, _ := json.Marshal(e)
				w.Write(append(raw, '\n'))
			}
			err = w.Flush()
		}
		if err == nil {
			err = f.Sync()
		}
		f.Close()
		if err != nil {
			for _, e := range fresh {
				delete(l.seen, e.ID)
			}
			return 0, err
		}
	}
	l.events = append(l.events, fresh...)
	return len(fresh), nil
}

// Events returns a time-ordered copy.
func (l *EventLog) Events() []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := append([]Event{}, l.events...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}
