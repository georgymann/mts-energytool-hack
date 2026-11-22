// Package expert holds the business rules of the specialist path:
// knowledge_gap -> request -> specialist reply to the client -> reviewed knowledge
// entry -> reuse in search. It depends only on the standard library and its own
// DTOs; HTTP, roles from the trusted server context, storage, locking, delivery
// and search activation belong to the core API.
package expert

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrVersionConflict     = errors.New("version is stale")
	ErrRequestConflict     = errors.New("command id was used for a different command")
	ErrInvalidTransition   = errors.New("action is not allowed in the current state")
	ErrForbidden           = errors.New("actor is not allowed to perform this action")
	ErrInvalidInput        = errors.New("invalid input")
	ErrPrivateData         = errors.New("text contains private data")
	ErrChecklistIncomplete = errors.New("review checklist is incomplete")
)

type Role string

const (
	RoleCoordinator Role = "coordinator" // assigns requests
	RoleSpecialist  Role = "specialist"  // answers assigned requests, drafts knowledge
	RoleReviewer    Role = "reviewer"    // approves and revokes knowledge
	RoleSystem      Role = "system"      // delivery and activation callbacks from the core API
)

// Actor must come from the trusted server context, never from a client request body.
type Actor struct {
	ID   string `json:"id"`
	Role Role   `json:"role"`
}

func (a Actor) valid() bool {
	switch a.Role {
	case RoleCoordinator, RoleSpecialist, RoleReviewer, RoleSystem:
		return strings.TrimSpace(a.ID) != ""
	}
	return false
}

// Command carries the optimistic-lock version and an idempotency id.
type Command struct {
	ExpectedVersion int    `json:"expected_version"`
	ID              string `json:"command_id"`
}

// replayLog remembers recent command ids with a fingerprint of their content:
// the same id and content replays (no-op); the same id with other content conflicts.
type replayLog struct {
	IDs    map[string]int    `json:"command_ids,omitempty"`
	Hashes map[string]string `json:"command_hashes,omitempty"`
}

const maxRemembered = 200

func fingerprint(op string, parts ...interface{}) string {
	raw, _ := json.Marshal(append([]interface{}{op}, parts...))
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func (r *replayLog) replay(id, hash string) (bool, error) {
	if id == "" {
		return false, nil
	}
	stored, ok := r.Hashes[id]
	if !ok {
		if _, known := r.IDs[id]; known {
			return false, ErrRequestConflict
		}
		return false, nil
	}
	if stored != hash {
		return false, ErrRequestConflict
	}
	return true, nil
}

func (r *replayLog) remember(id, hash string, version int) {
	if id == "" {
		return
	}
	if r.IDs == nil {
		r.IDs = map[string]int{}
	}
	if r.Hashes == nil {
		r.Hashes = map[string]string{}
	}
	if len(r.IDs) >= maxRemembered {
		oldest, min := "", int(^uint(0)>>1)
		for k, v := range r.IDs {
			if v < min || v == min && k < oldest {
				oldest, min = k, v
			}
		}
		delete(r.IDs, oldest)
		delete(r.Hashes, oldest)
	}
	r.IDs[id], r.Hashes[id] = version, hash
}

// checkCommand also guards callbacks whose target state has already been reached.
// Such callbacks must not bypass role, replay or optimistic-lock validation.
func checkCommand(log replayLog, version int, actor Actor, cmd Command, hash string) (bool, error) {
	if !actor.valid() {
		return false, ErrForbidden
	}
	if strings.TrimSpace(cmd.ID) == "" {
		return false, fmt.Errorf("%w: command id is required", ErrInvalidInput)
	}
	if replay, err := log.replay(cmd.ID, hash); replay || err != nil {
		return replay, err
	}
	if cmd.ExpectedVersion != version {
		return false, ErrVersionConflict
	}
	return false, nil
}

func (r replayLog) clone() replayLog {
	out := replayLog{IDs: map[string]int{}, Hashes: map[string]string{}}
	for k, v := range r.IDs {
		out.IDs[k] = v
	}
	for k, v := range r.Hashes {
		out.Hashes[k] = v
	}
	return out
}

func cleanText(s string, max int) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > max {
		return "", fmt.Errorf("%w: text must be 1..%d characters", ErrInvalidInput, max)
	}
	return s, nil
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

func normalize(s string) string {
	s = strings.ToLower(strings.ReplaceAll(s, "ё", "е"))
	return strings.Join(strings.Fields(strings.Trim(s, " .,!?;:")), " ")
}

func utc(t time.Time) time.Time { return t.UTC() }
