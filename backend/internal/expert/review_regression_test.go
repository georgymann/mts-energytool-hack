package expert

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDeliveredReplayValidatesCommandAndActor(t *testing.T) {
	r := answered(t)
	c := cmd(r, "delivered")
	r = ok(t)(MarkDelivered(r, system, c, r.Reply.ID, false, t0))
	cases := []struct {
		name    string
		actor   Actor
		command Command
		closed  bool
		want    error
	}{
		{"role", specialist, cmd(r, "other"), false, ErrForbidden},
		{"stale", system, Command{ID: "other", ExpectedVersion: 1}, false, ErrVersionConflict},
		{"changed command", system, c, true, ErrRequestConflict},
		{"changed target", system, cmd(r, "other"), true, ErrInvalidTransition},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := MarkDelivered(r, tc.actor, tc.command, r.Reply.ID, tc.closed, t0); !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

func TestActivationReplayValidatesCommandActorAndIncludedRevision(t *testing.T) {
	e, _ := approvedEntry(t)
	result := ActivationResult{KBVersion: "kb-7", OK: true, Included: map[string]int{e.ID: 1}}
	c := ecmd(e, "activate")
	e = okE(t)(ApplyActivation(e, system, c, result, t0))
	if _, err := ApplyActivation(e, author, ecmd(e, "other"), result, t0); !errors.Is(err, ErrForbidden) {
		t.Errorf("role: %v", err)
	}
	if _, err := ApplyActivation(e, system, Command{ID: "other", ExpectedVersion: 1}, result, t0); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale: %v", err)
	}
	changed := result
	changed.ErrorCode = "different body"
	if _, err := ApplyActivation(e, system, c, changed, t0); !errors.Is(err, ErrRequestConflict) {
		t.Errorf("changed body: %v", err)
	}
	e = okE(t)(ApplyActivation(e, system, ecmd(e, "remove"), ActivationResult{KBVersion: "kb-8", OK: true}, t0))
	if _, err := ApplyActivation(e, system, ecmd(e, "unknown"), ActivationResult{KBVersion: "kb-9", OK: true, Included: map[string]int{e.ID: 99}}, t0); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("nonexistent included revision: %v", err)
	}
}

func TestMemoryStoresDoNotShareMutablePointers(t *testing.T) {
	s := NewMemoryStore()
	r := answered(t)
	r = ok(t)(MarkDelivered(r, system, cmd(r, "deliver"), r.Reply.ID, false, t0))
	if err := s.Save(r, 0); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(r.ID)
	*got.Reply.DeliveredAt = t0.Add(time.Hour)
	got, _ = s.Get(r.ID)
	if !got.Reply.DeliveredAt.Equal(t0) {
		t.Error("Get mutated stored delivery timestamp without Save")
	}
	e, _ := approvedEntry(t)
	ks := KnowledgeMemory{s}
	if err := ks.Save(e, 0); err != nil {
		t.Fatal(err)
	}
	v, _ := ks.Get(e.ID)
	v.Revisions[0].Checklist.NoPrivateData = false
	*v.Revisions[0].ReviewedAt = t0.Add(time.Hour)
	v, _ = ks.Get(e.ID)
	if !v.Revisions[0].Checklist.NoPrivateData || !v.Revisions[0].ReviewedAt.Equal(t0) {
		t.Error("Get mutated stored approval without Save")
	}
}

func TestDemoOriginCannotCreateProductionKnowledge(t *testing.T) {
	r := answered(t)
	e := okE(t)(NewDraft(&r, generic(), author, "kb-demo", false, t0))
	e = okE(t)(Approve(e, reviewer, ecmd(e, "approve"), 1, full, t0))
	if !e.Test || len(ActiveSet([]KnowledgeEntry{e}, false)) != 0 {
		t.Fatal("demo source entered production export")
	}
}

func TestKnowledgeEditsRequireOriginalSource(t *testing.T) {
	e, src := approvedEntry(t)
	wrong := src
	wrong.ID = "another-request"
	for _, source := range []*Request{nil, &wrong} {
		in := generic()
		in.Basis = "Сведения из " + src.SessionID
		if _, err := EditDraft(e, source, author, ecmd(e, "edit"), in, t0); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("missing/wrong source must fail closed: %v", err)
		}
	}
}

func TestKnowledgeSourcePathAndSingleFragment(t *testing.T) {
	for _, id := range []string{"../private", "a/b", "a\\b", "a#s001", "a\nheading"} {
		if _, err := NewDraft(nil, generic(), author, id, true, t0); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("unsafe id %q accepted: %v", id, err)
		}
	}
	in := generic()
	in.Title += "\n## Заголовок"
	in.Answer += "\n## Вложенный заголовок\nУсловие. \\\\## Ещё заголовок\x1c## Условие"
	e := okE(t)(NewDraft(nil, in, author, "kb-safe", true, t0))
	e = okE(t)(Approve(e, reviewer, ecmd(e, "approve"), 1, full, t0))
	doc := RenderForKB(e, *e.InForce())
	if strings.Contains(doc.Markdown, "\n## ") || strings.Contains(doc.Markdown, "\\## ") || strings.Contains(doc.Markdown, "\x1c") {
		t.Fatal("rendered text creates additional loader sections")
	}
	in = generic()
	in.Answer = strings.Repeat("Описание ", 350)
	in.Basis = strings.Repeat("Основание ", 300)
	if _, err := NewDraft(nil, in, author, "kb-long", true, t0); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("document exceeding one loader fragment accepted: %v", err)
	}
}

func TestCommandsRequireIDsAndRejectUnverifiableReplay(t *testing.T) {
	r := register(t)
	if _, err := Assign(r, coordinator, cmd(r, " "), "spec-1", t0); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("empty id: %v", err)
	}
	r.Commands.IDs = map[string]int{"old": 1}
	if _, err := Assign(r, coordinator, cmd(r, "old"), "spec-1", t0); !errors.Is(err, ErrRequestConflict) {
		t.Errorf("replay without saved hash: %v", err)
	}
}

func TestRequestKeyPreservesPairBoundaries(t *testing.T) {
	if RequestKey("session|message", "id") == RequestKey("session", "message|id") {
		t.Fatal("different sessions share a uniqueness key")
	}
}

func TestConfirmationsRememberNewCommandIDs(t *testing.T) {
	r := answered(t)
	r = ok(t)(MarkDelivered(r, system, cmd(r, "delivered"), r.Reply.ID, false, t0))
	c := cmd(r, "confirmation")
	r = ok(t)(MarkDelivered(r, system, c, r.Reply.ID, false, t0))
	if _, err := MarkDelivered(r, system, c, r.Reply.ID, true, t0); !errors.Is(err, ErrRequestConflict) {
		t.Errorf("delivery confirmation must reserve its command ID: %v", err)
	}
	if r.Reply.DeliveryAttempts != 1 {
		t.Fatal("confirmation must not deliver again")
	}
	e, _ := approvedEntry(t)
	result := ActivationResult{KBVersion: "kb-7", OK: true, Included: map[string]int{e.ID: 1}}
	e = okE(t)(ApplyActivation(e, system, ecmd(e, "activate"), result, t0))
	kc := ecmd(e, "confirm")
	e = okE(t)(ApplyActivation(e, system, kc, result, t0))
	result.ErrorCode = "different body"
	if _, err := ApplyActivation(e, system, kc, result, t0); !errors.Is(err, ErrRequestConflict) {
		t.Errorf("activation confirmation must reserve its command ID: %v", err)
	}
}
