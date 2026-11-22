package expert

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var (
	author   = Actor{ID: "spec-1", Role: RoleSpecialist}
	reviewer = Actor{ID: "spec-1", Role: RoleReviewer} // same person, separate recorded role
	full     = ReviewChecklist{true, true, true, true, true}
)

// generic: a reusable statement without the client's private details (warehouse in Kazan).
func generic() DraftInput {
	return DraftInput{
		Title:               "Согласование солнечной станции на кровле с МЧС",
		GeneralizedQuestion: "Нужно ли отдельно согласовывать сетевую солнечную станцию на кровле с пожарным надзором?",
		Answer:              "ТЕСТОВЫЕ ДАННЫЕ. Требования пожарной безопасности проверяются на этапе проектирования станции.",
		Applicability:       "Сетевые станции 15–500 кВт на кровле коммерческих объектов. Не относится к наземным станциям.",
		Basis:               "Тестовая запись для демонстрации; реальное знание подтверждает профильный специалист.",
	}
}

func okE(t *testing.T) func(KnowledgeEntry, error) KnowledgeEntry {
	return func(e KnowledgeEntry, err error) KnowledgeEntry {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
}

func ecmd(e KnowledgeEntry, id string) Command { return Command{ExpectedVersion: e.Version, ID: id} }

func approvedEntry(t *testing.T) (KnowledgeEntry, Request) {
	t.Helper()
	src := answered(t)
	e := okE(t)(NewDraft(&src, generic(), author, "kb-1", true, t0))
	e = okE(t)(Approve(e, reviewer, ecmd(e, "ap1"), 1, full, t0))
	return e, src
}

func TestDraftRejectsPrivateDataAndCopies(t *testing.T) {
	src := answered(t)
	cases := map[string]func(*DraftInput){
		"phone":      func(d *DraftInput) { d.Answer += " Звоните +7 (900) 000-00-00." },
		"email":      func(d *DraftInput) { d.Basis = "письмо test@example.com" },
		"session id": func(d *DraftInput) { d.Basis = "обращение из сессии " + src.SessionID },
		"uuid":       func(d *DraftInput) { d.Basis = "см. 8f1c2a90-3b4d-4e5f-9a10-2b3c4d5e6f70" },
		"client question": func(d *DraftInput) {
			d.GeneralizedQuestion = "а нужно ли согласовывать станцию с МЧС"
		},
		"client reply": func(d *DraftInput) { d.Answer = src.Reply.Body },
	}
	for name, mutate := range cases {
		in := generic()
		mutate(&in)
		if _, err := NewDraft(&src, in, author, "kb", true, t0); !errors.Is(err, ErrPrivateData) {
			t.Errorf("%s must be rejected, got %v", name, err)
		}
	}
	in := generic()
	in.Applicability = " "
	if _, err := NewDraft(&src, in, author, "kb", true, t0); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("applicability is required")
	}
	if _, err := NewDraft(&src, generic(), Actor{ID: "x", Role: RoleSystem}, "kb", true, t0); !errors.Is(err, ErrForbidden) {
		t.Fatal("system cannot author knowledge")
	}
}

func TestApprovalIsHumanAndRecorded(t *testing.T) {
	src := answered(t)
	e := okE(t)(NewDraft(&src, generic(), author, "kb-1", true, t0))
	if len(ActiveSet([]KnowledgeEntry{e}, true)) != 0 {
		t.Fatal("a draft must never be offered to search")
	}
	if _, err := Approve(e, author, ecmd(e, "a0"), 1, full, t0); !errors.Is(err, ErrForbidden) {
		t.Fatal("only the reviewer role approves")
	}
	partial := full
	partial.NoPrivateData = false
	if _, err := Approve(e, reviewer, ecmd(e, "a1"), 1, partial, t0); !errors.Is(err, ErrChecklistIncomplete) {
		t.Fatal("every checklist item must be confirmed")
	}
	e = okE(t)(Approve(e, reviewer, ecmd(e, "a2"), 1, full, t0.Add(time.Hour)))
	rev := e.Revisions[0]
	if rev.Publication != PubApproved || rev.Runtime != RuntimePending || rev.ReviewerID != "spec-1" || rev.ReviewedAt == nil ||
		e.History[1].Role != RoleReviewer || e.History[0].Role != RoleSpecialist {
		t.Fatalf("approval, reviewer and roles recorded separately: %+v %+v", rev, e.History)
	}
	if _, ok := AvailableToBot(e); ok {
		t.Fatal("approved is not «available to bot» before activation")
	}
	again, err := Approve(e, reviewer, Command{ExpectedVersion: 1, ID: "a2"}, 1, full, t0)
	if err != nil || again.Version != e.Version {
		t.Fatal("a retried approval is a no-op")
	}
}

func TestActivationReplacementFailureAndRevocation(t *testing.T) {
	e, src := approvedEntry(t)
	sys := Actor{ID: "web-backend", Role: RoleSystem}
	docs := ActiveSet([]KnowledgeEntry{e}, true)
	if len(docs) != 1 || docs[0].SourceID != "90-expert/kb-1-r1.md#s001" {
		t.Fatalf("approved entry offered once: %+v", docs)
	}
	activationCommand := ecmd(e, "act1")
	e = okE(t)(ApplyActivation(e, sys, activationCommand, ActivationResult{KBVersion: "kb-7", OK: true, Included: map[string]int{"kb-1": 1}}, t0))
	if r, ok := AvailableToBot(e); !ok || r.KBVersion != "kb-7" || r.Revision != 1 {
		t.Fatalf("active with the real kb_version: %+v", e.Revisions)
	}
	same, err := ApplyActivation(e, sys, activationCommand, ActivationResult{KBVersion: "kb-7", OK: true, Included: map[string]int{"kb-1": 1}}, t0)
	if err != nil || same.Version != e.Version {
		t.Fatal("re-applying the same kb_version is a no-op")
	}

	// Edit -> new draft revision; revision 1 stays in force until replacement is active.
	in := generic()
	in.Answer = "ТЕСТОВЫЕ ДАННЫЕ. Уточнённая формулировка: требования проверяются при проектировании."
	e = okE(t)(EditDraft(e, &src, author, ecmd(e, "ed1"), in, t0))
	if len(e.Revisions) != 2 || e.Revisions[1].Publication != PubDraft || e.InForce().Revision != 1 {
		t.Fatalf("new draft revision must not displace the approved one: %+v", e.Revisions)
	}
	e = okE(t)(Approve(e, reviewer, ecmd(e, "ap2"), 2, full, t0))
	if docs := ActiveSet([]KnowledgeEntry{e}, true); len(docs) != 1 || docs[0].Revision != 2 {
		t.Fatalf("exactly one revision per entry in the next base: %+v", docs)
	}
	// Activation of revision 2 fails: rev 2 failed and visible, rev 1 keeps serving.
	e = okE(t)(ApplyActivation(e, sys, ecmd(e, "act2"), ActivationResult{KBVersion: "kb-8", OK: false, ErrorCode: "index_build_failed"}, t0))
	if e.Revisions[1].Runtime != RuntimeFailed || e.Revisions[1].Publication != PubApproved || e.Revisions[0].Runtime != RuntimeActive {
		t.Fatalf("failure visible, old revision still active: %+v", e.Revisions)
	}
	e = okE(t)(ApplyActivation(e, sys, ecmd(e, "act3"), ActivationResult{KBVersion: "kb-9", OK: true, Included: map[string]int{"kb-1": 2}}, t0))
	active := 0
	for _, r := range e.Revisions {
		if r.Runtime == RuntimeActive {
			active++
		}
	}
	if active != 1 || e.Revisions[1].Runtime != RuntimeActive || e.Revisions[0].Runtime != RuntimeInactive {
		t.Fatalf("one active revision after replacement: %+v", e.Revisions)
	}

	// Revocation: out of the next base; deactivation pending until confirmed.
	if _, err := Revoke(e, reviewer, ecmd(e, "rv0"), " ", t0); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("revocation needs a reason")
	}
	e = okE(t)(Revoke(e, reviewer, ecmd(e, "rv1"), "Сведения устарели", t0))
	if len(ActiveSet([]KnowledgeEntry{e}, true)) != 0 || e.Revisions[1].Runtime != RuntimePending {
		t.Fatalf("revoked entry excluded, deactivation pending: %+v", e.Revisions)
	}
	e = okE(t)(ApplyActivation(e, sys, ecmd(e, "act4"), ActivationResult{KBVersion: "kb-10", OK: true, Included: map[string]int{}}, t0))
	if _, ok := AvailableToBot(e); ok || e.Revisions[1].Runtime != RuntimeInactive || len(e.History) < 8 {
		t.Fatalf("revoked entry inactive, history kept: %+v", e.Revisions)
	}
}

func TestPrivateReplyStaysSeparateFromSharedKnowledge(t *testing.T) {
	e, src := approvedEntry(t)
	doc := RenderForKB(e, *e.InForce())
	if !strings.Contains(src.Reply.Body, "Казани") || strings.Contains(doc.Markdown, "Казан") {
		t.Fatal("the client's private condition stays in the reply, not in the shared source")
	}
	for _, id := range []string{src.ID, src.SessionID, src.OriginMessageID, "spec-1", "lead-1"} {
		if strings.Contains(doc.Markdown, id) || strings.Contains(doc.SourceID, id) {
			t.Fatalf("source must not reveal %q:\n%s", id, doc.Markdown)
		}
	}
	if e.SourceRequestID != src.ID {
		t.Fatal("the link to the request is kept internally")
	}
}

func TestTestKnowledgeGoesOnlyToTheTestBase(t *testing.T) {
	e, _ := approvedEntry(t)
	if len(ActiveSet([]KnowledgeEntry{e}, false)) != 0 || len(ActiveSet([]KnowledgeEntry{e}, true)) != 1 {
		t.Fatal("demo entries must stay out of the real base")
	}
}
