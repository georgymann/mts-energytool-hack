package expert

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

// KBDocument is one approved revision rendered for the web search loader.
// It carries no author, reviewer, request, session or contact data.
type KBDocument struct {
	EntryID  string `json:"entry_id"`
	Revision int    `json:"revision"`
	Path     string `json:"path"`      // 90-expert/<entry_id>-r<revision>.md
	SourceID string `json:"source_id"` // path#s001 as produced by the Markdown section loader
	Markdown string `json:"markdown"`
	Test     bool   `json:"test"`
}

// RenderForKB renders a revision as one Markdown document with a single section.
func RenderForKB(e KnowledgeEntry, rev Revision) KBDocument {
	path := fmt.Sprintf("90-expert/%s-r%d.md", e.ID, rev.Revision)
	return KBDocument{EntryID: e.ID, Revision: rev.Revision, Path: path, SourceID: path + "#s001",
		Markdown: renderMarkdown(rev), Test: e.Test}
}

// Render field values as single paragraphs. Nested Markdown headings (including
// the legacy escaped-heading syntax understood by the loader) must not separate
// an answer from its applicability or basis into different retrieval fragments.
func markdownField(s string) string {
	// Python splitlines also recognizes the ASCII file/group/record separators.
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || r == '\x1c' || r == '\x1d' || r == '\x1e'
	}), " ")
	for strings.Contains(s, `\## `) {
		s = strings.ReplaceAll(s, `\## `, "## ")
	}
	return s
}

func renderMarkdown(rev Revision) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Ответ специалиста: %s\n\n", markdownField(rev.Title))
	fmt.Fprintf(&b, "Вопрос: %s\n\n", markdownField(rev.GeneralizedQuestion))
	fmt.Fprintf(&b, "Ответ: %s\n\n", markdownField(rev.Answer))
	fmt.Fprintf(&b, "Применимо: %s\n\n", markdownField(rev.Applicability))
	fmt.Fprintf(&b, "Основание: %s\n", markdownField(rev.Basis))
	if rev.ReviewedAt != nil {
		fmt.Fprintf(&b, "\nПроверено специалистом Energytool: %s\n", rev.ReviewedAt.In(time.FixedZone("MSK", 3*3600)).Format("02.01.2006"))
	}
	return b.String()
}

// ActiveSet lists the documents the next knowledge-base version must contain:
// exactly one revision per entry (the one in force), drafts and revoked entries excluded.
// test selects the separate demo base (true) or the real base (false).
func ActiveSet(entries []KnowledgeEntry, test bool) []KBDocument {
	out := []KBDocument{}
	for _, e := range entries {
		if e.Test != test {
			continue
		}
		if rev := e.InForce(); rev != nil {
			out = append(out, RenderForKB(e, *rev))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// ActivationResult is reported by the core API after it rebuilt (or failed to rebuild) the search.
type ActivationResult struct {
	KBVersion string         `json:"kb_version"`
	OK        bool           `json:"ok"`
	ErrorCode string         `json:"error_code,omitempty"`
	Included  map[string]int `json:"included"` // entry id -> revision actually in kb_version
}

// ApplyActivation records the real search state for one entry (system actor). On success the
// included revision becomes active and every other one inactive; on failure pending revisions
// become failed while a previously active revision stays active (the old base still serves).
// Exact command retries are no-ops. A new confirmation command is recorded even
// when the same kb_version is already active, so its id cannot be reused.
func ApplyActivation(e KnowledgeEntry, actor Actor, cmd Command, result ActivationResult, now time.Time) (KnowledgeEntry, error) {
	if actor.Role != RoleSystem {
		return e, ErrForbidden
	}
	hash := fingerprint("activation", cmd.ExpectedVersion, actor, result)
	if replay, err := checkCommand(e.Commands, e.Version, actor, cmd, hash); replay || err != nil {
		return e, err
	}
	if result.OK {
		if result.KBVersion == "" {
			return e, fmt.Errorf("%w: kb_version is required", ErrInvalidInput)
		}
		if included := result.Included[e.ID]; included != 0 {
			found := false
			for _, r := range e.Revisions {
				found = found || (r.Revision == included && r.Publication == PubApproved)
			}
			if !found {
				return e, fmt.Errorf("%w: activated revision is not approved", ErrInvalidTransition)
			}
		}
	}
	alreadyApplied := false
	if result.OK && result.KBVersion != "" {
		applied := true
		included := result.Included[e.ID]
		for _, r := range e.Revisions {
			want := RuntimeInactive
			if r.Revision == included {
				want = RuntimeActive
			}
			if r.Publication == PubDraft {
				continue
			}
			if r.Runtime != want || r.KBVersion != result.KBVersion {
				applied = false
			}
		}
		alreadyApplied = applied
	}
	return applyKnowledge(e, actor, cmd, hash, now,
		func(n *KnowledgeEntry) (string, int, error) {
			included := result.Included[n.ID]
			if alreadyApplied {
				return "activation_confirmed", included, nil
			}
			for i := range n.Revisions {
				r := &n.Revisions[i]
				if r.Publication == PubDraft {
					continue
				}
				switch {
				case result.OK && r.Revision == included:
					r.Runtime, r.KBVersion, r.RuntimeError = RuntimeActive, result.KBVersion, ""
				case result.OK:
					if r.Runtime != RuntimeNone {
						r.Runtime, r.KBVersion, r.RuntimeError = RuntimeInactive, result.KBVersion, ""
					}
				case r.Runtime == RuntimePending:
					r.Runtime, r.RuntimeError = RuntimeFailed, truncate(result.ErrorCode, 100)
				}
			}
			action := "activation_failed"
			if result.OK {
				action = "activated"
			}
			return action, included, nil
		})
}

// AvailableToBot is true only for a revision active in a confirmed kb_version.
func AvailableToBot(e KnowledgeEntry) (Revision, bool) {
	for _, r := range e.Revisions {
		if r.Runtime == RuntimeActive && r.Publication == PubApproved && r.KBVersion != "" {
			return r, true
		}
	}
	return Revision{}, false
}

// PublicEntry hides command bookkeeping; the internal source request id stays for staff views only.
func PublicEntry(e KnowledgeEntry) KnowledgeEntry {
	out := cloneEntry(e)
	out.Commands = replayLog{}
	return out
}
