package business

import (
	"fmt"
	"testing"
	"time"
)

// agreedExample builds the reference case from the task: 10 sessions with questions,
// 4 with applications; 2 accepted, 1 rejected, 1 needs_info. Built through the real
// SaveApplication/Transition functions, not hand-written numbers.
func agreedExample(t *testing.T, demo bool) []Event {
	t.Helper()
	base := time.Date(2026, 11, 13, 10, 0, 0, 0, Moscow)
	var events []Event
	outcomes := []Action{ActionAccept, ActionAccept, ActionReject, ActionNeedsInfo}
	for i := 0; i < 10; i++ {
		sid := fmt.Sprintf("s%d", i)
		start := base.Add(time.Duration(i) * time.Hour)
		events = append(events, SessionStarted(sid, demo, start),
			ConsultationRequested(sid, sid+"-m1", demo, start.Add(10*time.Second)),
			FirstAnswerReceived(sid, demo, start.Add(20*time.Second)))
		if i >= 4 {
			continue
		}
		session := fixtureSession()
		session.ID = sid
		saveAt := start.Add(time.Duration(i+1) * time.Minute) // 1..4 minutes after start
		rec, ev, err := SaveApplication(nil, session, "app-"+sid, demo, saveAt)
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, ev...)
		rec, ev, _ = Transition(rec, ActionStartReview, "", "", rec.Version, "", saveAt.Add(time.Hour))
		events = append(events, ev...)
		reason := ReasonCode("")
		if outcomes[i] != ActionAccept {
			reason = "missing_data"
			if outcomes[i] == ActionReject {
				reason = "not_target"
			}
		}
		_, ev, err = Transition(rec, outcomes[i], reason, "", rec.Version, "", saveAt.Add(time.Hour+10*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, ev...)
	}
	return events
}

func TestAgreedExample(t *testing.T) {
	events := agreedExample(t, false)
	m := ComputePilotMetrics(events, time.Time{}, time.Time{}, false)
	if m.SessionsWithQuestion != 10 || m.SessionsWithApplication != 4 || *m.Conversion.Value != 0.4 || m.Conversion.Display != "40%" {
		t.Fatalf("conversion: %+v", m.Conversion)
	}
	if m.Acceptance.Numerator != 2 || m.Acceptance.Denominator != 3 || m.Acceptance.Display != "66,7%" {
		t.Fatalf("acceptance 2/3: %+v", m.Acceptance)
	}
	if m.StatusCounts["needs_info"] != 1 || m.StatusCounts["rejected"] != 1 || m.StatusCounts["accepted"] != 2 {
		t.Fatalf("status counts: %+v", m.StatusCounts)
	}
	if m.ReturnReasons["missing_data"] != 1 || m.RejectReasons["not_target"] != 1 {
		t.Fatalf("reasons: %+v %+v", m.ReturnReasons, m.RejectReasons)
	}
	if m.SessionsWithoutApplication.Numerator != 6 {
		t.Fatalf("losses: %+v", m.SessionsWithoutApplication)
	}
	// Saved 1..4 min after start, request at +10 s -> 50, 110, 170, 230 s.
	if m.TimeToApplication.N != 4 || *m.TimeToApplication.MedianSeconds != 140 || *m.TimeToApplication.P95Seconds != 230 {
		t.Fatalf("time to application: %+v", m.TimeToApplication)
	}
	// Decision 10 min after review start, only for accepted/rejected (N=3).
	if m.ReviewTime.N != 3 || *m.ReviewTime.MedianSeconds != 600 {
		t.Fatalf("review time: %+v", m.ReviewTime)
	}
}

func TestReplaysAndResavesDoNotChangeMetrics(t *testing.T) {
	events := agreedExample(t, false)
	base := ComputePilotMetrics(events, time.Time{}, time.Time{}, false)
	replayed := append(append([]Event{}, events...), events...)
	again := ComputePilotMetrics(replayed, time.Time{}, time.Time{}, false)
	if *again.Conversion.Value != *base.Conversion.Value || *again.Acceptance.Value != *base.Acceptance.Value ||
		again.Acceptance.Denominator != base.Acceptance.Denominator || again.StatusCounts["accepted"] != 2 {
		t.Fatalf("duplicates changed metrics: %+v", again)
	}
	// A second save of an already accepted application (new version) is not a new application.
	extra := applicationEvent(EventApplicationSaved, ApplicationRecord{ID: "app-s0", SessionID: "s0", Version: 9}, time.Now())
	withResave := ComputePilotMetrics(append(events, extra), time.Time{}, time.Time{}, false)
	if withResave.SessionsWithApplication != 4 || withResave.StatusCounts["accepted"] != 2 {
		t.Fatalf("resave changed metrics: %+v", withResave.StatusCounts)
	}
}

func TestEmptyAndDemoData(t *testing.T) {
	empty := ComputePilotMetrics(nil, time.Time{}, time.Time{}, false)
	if empty.Conversion.Value != nil || empty.Conversion.Display != "нет данных" || empty.Acceptance.Display != "нет данных" ||
		empty.TimeToApplication.Display != "нет данных" || empty.ReviewTime.N != 0 {
		t.Fatalf("empty denominators must be «нет данных»: %+v", empty)
	}
	demo := agreedExample(t, true)
	real := ComputePilotMetrics(demo, time.Time{}, time.Time{}, false)
	if real.SessionsWithQuestion != 0 || real.ExcludedDemoEvt == 0 {
		t.Fatalf("demo events must be excluded by default: %+v", real)
	}
	withDemo := ComputePilotMetrics(demo, time.Time{}, time.Time{}, true)
	if withDemo.SessionsWithQuestion != 10 || !withDemo.IncludesDemo {
		t.Fatalf("demo can be included explicitly: %+v", withDemo)
	}
}

func TestMoscowPeriodBoundaries(t *testing.T) {
	// 23:30 MSK on 12.11 is 20:30 UTC: belongs to 12.11 in Moscow.
	late := time.Date(2026, 11, 12, 20, 30, 0, 0, time.UTC)
	events := []Event{SessionStarted("a", false, late), ConsultationRequested("a", "a1", false, late),
		SessionStarted("b", false, late.Add(time.Hour)), ConsultationRequested("b", "b1", false, late.Add(time.Hour))}
	day12 := time.Date(2026, 11, 12, 0, 0, 0, 0, Moscow)
	m := ComputePilotMetrics(events, day12, day12.AddDate(0, 0, 1), false)
	if m.SessionsWithQuestion != 1 || m.SessionsStarted != 1 {
		t.Fatalf("only the 23:30 MSK session belongs to 12.11: %+v", m)
	}
}
