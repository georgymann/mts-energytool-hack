package expert

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

type PublicationStatus string

const (
	PubDraft    PublicationStatus = "draft"
	PubApproved PublicationStatus = "approved" // approved by a human reviewer; not yet "in search"
	PubRevoked  PublicationStatus = "revoked"
)

// RuntimeStatus is the state in the web search, reported by the core API's activation.
type RuntimeStatus string

const (
	RuntimeNone     RuntimeStatus = ""         // draft: never offered to search
	RuntimePending  RuntimeStatus = "pending"  // approved or revoked, search not updated yet
	RuntimeActive   RuntimeStatus = "active"   // part of the active kb_version
	RuntimeFailed   RuntimeStatus = "failed"   // activation failed; still approved, error visible
	RuntimeInactive RuntimeStatus = "inactive" // replaced by a newer active revision, or revoked and removed
)

type ReviewChecklist struct {
	FactConfirmed    bool `json:"fact_confirmed"`
	ConditionsStated bool `json:"conditions_stated"`
	NoContradiction  bool `json:"no_contradiction"`
	NoPrivateData    bool `json:"no_private_data"`
	Reusable         bool `json:"reusable"`
}

func (c ReviewChecklist) complete() bool {
	return c.FactConfirmed && c.ConditionsStated && c.NoContradiction && c.NoPrivateData && c.Reusable
}

type DraftInput struct {
	Title               string `json:"title"`
	GeneralizedQuestion string `json:"generalized_question"`
	Answer              string `json:"answer"`
	Applicability       string `json:"applicability"`
	Basis               string `json:"basis"`
}

type Revision struct {
	Revision            int               `json:"revision"`
	Title               string            `json:"title"`
	GeneralizedQuestion string            `json:"generalized_question"`
	Answer              string            `json:"answer"`
	Applicability       string            `json:"applicability"`
	Basis               string            `json:"basis"`
	AuthorID            string            `json:"author_id"`
	CreatedAt           time.Time         `json:"created_at"`
	ReviewerID          string            `json:"reviewer_id,omitempty"`
	ReviewedAt          *time.Time        `json:"reviewed_at,omitempty"`
	Checklist           *ReviewChecklist  `json:"checklist,omitempty"`
	Publication         PublicationStatus `json:"publication_status"`
	Runtime             RuntimeStatus     `json:"runtime_status,omitempty"`
	KBVersion           string            `json:"kb_version,omitempty"`
	RuntimeError        string            `json:"runtime_error,omitempty"`
	RevokeReason        string            `json:"revoke_reason,omitempty"`
}

type KnowledgeHistory struct {
	Version  int       `json:"version"`
	Action   string    `json:"action"`
	Revision int       `json:"revision"`
	ActorID  string    `json:"actor_id"`
	Role     Role      `json:"role"`
	At       time.Time `json:"at"`
}

// KnowledgeEntry is a reviewed, generalized record — never a copy of a client reply.
// SourceRequestID stays internal and is never rendered into the search source.
type KnowledgeEntry struct {
	ID              string             `json:"id"`
	Test            bool               `json:"test"` // demo data: goes only to the separate test knowledge base
	SourceRequestID string             `json:"source_request_id,omitempty"`
	Revisions       []Revision         `json:"revisions"`
	Version         int                `json:"version"`
	CreatedAt       time.Time          `json:"created_at"`
	UpdatedAt       time.Time          `json:"updated_at"`
	History         []KnowledgeHistory `json:"history"`
	Commands        replayLog          `json:"commands"`
}

var (
	phonePattern   = regexp.MustCompile(`\+?\d[\d\s()\-]{8,}\d`)
	emailPattern   = regexp.MustCompile(`[\w.+-]+@[\w-]+\.[\w.-]+`)
	uuidPattern    = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	entryIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
)

const (
	maxTitle      = 200
	maxField      = 4000
	maxKBMarkdown = 4800 // one fragment in retrieval.load_fragments; includes labels and review date
)

// validateDraft checks required fields and rejects contacts, identifiers and an ungeneralized question.
func validateDraft(in DraftInput, source *Request) (DraftInput, error) {
	var err error
	if in.Title, err = cleanText(in.Title, maxTitle); err != nil {
		return in, fmt.Errorf("title: %w", err)
	}
	for name, field := range map[string]*string{"generalized_question": &in.GeneralizedQuestion, "answer": &in.Answer,
		"applicability": &in.Applicability, "basis": &in.Basis} {
		if *field, err = cleanText(*field, maxField); err != nil {
			return in, fmt.Errorf("%s: %w", name, err)
		}
	}
	all := strings.Join([]string{in.Title, in.GeneralizedQuestion, in.Answer, in.Applicability, in.Basis}, "\n")
	for _, m := range phonePattern.FindAllString(all, -1) {
		digits := 0
		for _, r := range m {
			if r >= '0' && r <= '9' {
				digits++
			}
		}
		if digits >= 10 {
			return in, fmt.Errorf("%w: phone number", ErrPrivateData)
		}
	}
	if emailPattern.MatchString(all) {
		return in, fmt.Errorf("%w: e-mail", ErrPrivateData)
	}
	if uuidPattern.MatchString(all) {
		return in, fmt.Errorf("%w: identifier", ErrPrivateData)
	}
	if source != nil {
		for _, id := range []string{source.ID, source.SessionID, source.OriginMessageID, source.ApplicationID} {
			if id != "" && strings.Contains(all, id) {
				return in, fmt.Errorf("%w: request/session identifier", ErrPrivateData)
			}
		}
		if normalize(in.GeneralizedQuestion) == normalize(source.Question) {
			return in, fmt.Errorf("%w: the client's question must be generalized, not copied", ErrPrivateData)
		}
		if source.Reply != nil && normalize(in.Answer) == normalize(source.Reply.Body) {
			return in, fmt.Errorf("%w: the client reply must be generalized, not copied", ErrPrivateData)
		}
	}
	preview := Revision{Title: in.Title, GeneralizedQuestion: in.GeneralizedQuestion, Answer: in.Answer,
		Applicability: in.Applicability, Basis: in.Basis}
	// Reserve room for the review date added after approval.
	if utf8.RuneCountInString(renderMarkdown(preview))+80 > maxKBMarkdown {
		return in, fmt.Errorf("%w: knowledge document must fit one %d-character search fragment", ErrInvalidInput, maxKBMarkdown)
	}
	return in, nil
}

func cloneEntry(e KnowledgeEntry) KnowledgeEntry {
	out := e
	out.Revisions = append([]Revision{}, e.Revisions...)
	for i := range out.Revisions {
		if e.Revisions[i].ReviewedAt != nil {
			at := *e.Revisions[i].ReviewedAt
			out.Revisions[i].ReviewedAt = &at
		}
		if e.Revisions[i].Checklist != nil {
			checklist := *e.Revisions[i].Checklist
			out.Revisions[i].Checklist = &checklist
		}
	}
	out.History = append([]KnowledgeHistory{}, e.History...)
	out.Commands = e.Commands.clone()
	return out
}

func (e KnowledgeEntry) latest() *Revision {
	if len(e.Revisions) == 0 {
		return nil
	}
	return &e.Revisions[len(e.Revisions)-1]
}

func authorRole(a Actor) bool {
	return a.Role == RoleSpecialist || a.Role == RoleCoordinator || a.Role == RoleReviewer
}

// NewDraft creates an entry with draft revision 1. source is optional (the answered request).
func NewDraft(source *Request, in DraftInput, author Actor, newID string, test bool, now time.Time) (KnowledgeEntry, error) {
	if !author.valid() || !authorRole(author) {
		return KnowledgeEntry{}, ErrForbidden
	}
	if !entryIDPattern.MatchString(newID) {
		return KnowledgeEntry{}, fmt.Errorf("%w: entry id must contain 1..128 ASCII letters, digits, hyphens or underscores and start with a letter or digit", ErrInvalidInput)
	}
	in, err := validateDraft(in, source)
	if err != nil {
		return KnowledgeEntry{}, err
	}
	at := utc(now)
	e := KnowledgeEntry{ID: newID, Test: test, Version: 1, CreatedAt: at, UpdatedAt: at,
		Revisions: []Revision{{Revision: 1, Title: in.Title, GeneralizedQuestion: in.GeneralizedQuestion, Answer: in.Answer,
			Applicability: in.Applicability, Basis: in.Basis, AuthorID: author.ID, CreatedAt: at, Publication: PubDraft}},
		History: []KnowledgeHistory{{Version: 1, Action: "draft", Revision: 1, ActorID: author.ID, Role: author.Role, At: at}}}
	if source != nil {
		e.SourceRequestID = source.ID
		e.Test = test || source.Demo
	}
	return e, nil
}

func applyKnowledge(e KnowledgeEntry, actor Actor, cmd Command, hash string, now time.Time,
	mutate func(next *KnowledgeEntry) (string, int, error)) (KnowledgeEntry, error) {
	if replay, err := checkCommand(e.Commands, e.Version, actor, cmd, hash); replay || err != nil {
		return e, err
	}
	next := cloneEntry(e)
	action, revision, err := mutate(&next)
	if err != nil {
		return e, err
	}
	next.Version++
	next.UpdatedAt = utc(now)
	next.History = append(next.History, KnowledgeHistory{Version: next.Version, Action: action, Revision: revision,
		ActorID: actor.ID, Role: actor.Role, At: utc(now)})
	next.Commands.remember(cmd.ID, hash, next.Version)
	return next, nil
}

// EditDraft updates the latest draft in place, or — if the latest revision is approved or
// revoked — opens a new draft revision. The approved revision keeps working meanwhile.
func EditDraft(e KnowledgeEntry, source *Request, actor Actor, cmd Command, in DraftInput, now time.Time) (KnowledgeEntry, error) {
	return applyKnowledge(e, actor, cmd, fingerprint("edit", cmd.ExpectedVersion, actor, in), now,
		func(n *KnowledgeEntry) (string, int, error) {
			if !authorRole(actor) {
				return "", 0, ErrForbidden
			}
			if (n.SourceRequestID != "" && (source == nil || source.ID != n.SourceRequestID)) ||
				(n.SourceRequestID == "" && source != nil) {
				return "", 0, fmt.Errorf("%w: edit requires the original source request", ErrInvalidInput)
			}
			valid, err := validateDraft(in, source)
			if err != nil {
				return "", 0, err
			}
			last := n.latest()
			rev := Revision{Title: valid.Title, GeneralizedQuestion: valid.GeneralizedQuestion, Answer: valid.Answer,
				Applicability: valid.Applicability, Basis: valid.Basis, AuthorID: actor.ID, CreatedAt: utc(now),
				Publication: PubDraft}
			if last.Publication == PubDraft {
				rev.Revision = last.Revision
				n.Revisions[len(n.Revisions)-1] = rev
				return "edit_draft", rev.Revision, nil
			}
			rev.Revision = last.Revision + 1
			n.Revisions = append(n.Revisions, rev)
			return "new_revision", rev.Revision, nil
		})
}

// Approve: a human reviewer approves the latest draft revision for the shared base.
// It becomes approved with runtime pending; it is not "in search" until activation.
func Approve(e KnowledgeEntry, reviewer Actor, cmd Command, revision int, checklist ReviewChecklist, now time.Time) (KnowledgeEntry, error) {
	return applyKnowledge(e, reviewer, cmd, fingerprint("approve", cmd.ExpectedVersion, reviewer, revision, checklist), now,
		func(n *KnowledgeEntry) (string, int, error) {
			if reviewer.Role != RoleReviewer {
				return "", 0, ErrForbidden
			}
			last := n.latest()
			if last.Revision != revision || last.Publication != PubDraft {
				return "", 0, fmt.Errorf("%w: only the latest draft revision can be approved", ErrInvalidTransition)
			}
			if !checklist.complete() {
				return "", 0, ErrChecklistIncomplete
			}
			at := utc(now)
			c := checklist
			last.Publication, last.ReviewerID, last.ReviewedAt, last.Checklist = PubApproved, reviewer.ID, &at, &c
			last.Runtime = RuntimePending
			return "approve", revision, nil
		})
}

// Revoke withdraws every approved revision. Active ones wait for deactivation (pending);
// never-activated ones become inactive at once. History and earlier answers stay as they are.
func Revoke(e KnowledgeEntry, reviewer Actor, cmd Command, reason string, now time.Time) (KnowledgeEntry, error) {
	return applyKnowledge(e, reviewer, cmd, fingerprint("revoke", cmd.ExpectedVersion, reviewer, reason), now,
		func(n *KnowledgeEntry) (string, int, error) {
			if reviewer.Role != RoleReviewer {
				return "", 0, ErrForbidden
			}
			text, err := cleanText(reason, 1000)
			if err != nil {
				return "", 0, fmt.Errorf("reason: %w", err)
			}
			revoked := 0
			for i := range n.Revisions {
				r := &n.Revisions[i]
				if r.Publication != PubApproved {
					continue
				}
				r.Publication, r.RevokeReason = PubRevoked, text
				if r.Runtime == RuntimeActive {
					r.Runtime = RuntimePending
				} else {
					r.Runtime = RuntimeInactive
				}
				revoked = r.Revision
			}
			if revoked == 0 {
				return "", 0, fmt.Errorf("%w: nothing approved to revoke", ErrInvalidTransition)
			}
			return "revoke", revoked, nil
		})
}

// InForce returns the revision that must be in search: the latest approved, unrevoked one.
// A newer draft does not displace it. Revoked entries have none.
func (e KnowledgeEntry) InForce() *Revision {
	for i := len(e.Revisions) - 1; i >= 0; i-- {
		if e.Revisions[i].Publication == PubApproved {
			return &e.Revisions[i]
		}
	}
	return nil
}
