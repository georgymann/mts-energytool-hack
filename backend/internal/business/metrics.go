package business

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Moscow is the reporting time zone for periods and cohorts (UTC+3, no DST).
var Moscow = time.FixedZone("Europe/Moscow", 3*60*60)

type Ratio struct {
	Numerator   int      `json:"numerator"`
	Denominator int      `json:"denominator"`
	Value       *float64 `json:"value"` // null when the denominator is 0
	Display     string   `json:"display"`
}

type DurationStats struct {
	N             int      `json:"n"`
	MedianSeconds *float64 `json:"median_seconds"`
	P95Seconds    *float64 `json:"p95_seconds"` // nearest-rank
	Display       string   `json:"display"`
}

type PilotMetrics struct {
	PeriodFrom      *time.Time `json:"period_from,omitempty"` // inclusive, Moscow
	PeriodTo        *time.Time `json:"period_to,omitempty"`   // exclusive, Moscow
	TimeZone        string     `json:"time_zone"`
	IncludesDemo    bool       `json:"includes_demo"`
	ExcludedDemoEvt int        `json:"excluded_demo_events"`

	SessionsStarted         int `json:"sessions_started"`
	SessionsWithQuestion    int `json:"sessions_with_question"` // cohort
	SessionsWithAnswer      int `json:"sessions_with_answer"`
	SessionsWithApplication int `json:"sessions_with_application"`

	// 1. Conversion = cohort sessions with a saved application / cohort sessions.
	Conversion Ratio `json:"conversion_to_application"`
	// 2. Acceptance = accepted / (accepted + rejected); needs_info and open shown separately.
	Acceptance   Ratio          `json:"acceptance_rate"`
	StatusCounts map[string]int `json:"status_counts"`
	// 3. Time to ready application = first application_saved - first user request.
	TimeToApplication DurationStats `json:"time_to_application"`
	// 4. Manager review time = final decision - first review_started (both present).
	ReviewTime DurationStats `json:"review_time"`
	// 5. Path losses: cohort sessions without an application + recorded reasons only.
	SessionsWithoutApplication Ratio          `json:"sessions_without_application"`
	ReturnReasons              map[string]int `json:"needs_info_reasons"`
	RejectReasons              map[string]int `json:"reject_reasons"`
	KnowledgeGaps              map[string]int `json:"knowledge_gaps"`
	Notes                      []string       `json:"notes"`
}

func ratio(num, den int) Ratio {
	r := Ratio{Numerator: num, Denominator: den, Display: "нет данных"}
	if den > 0 {
		v := float64(num) / float64(den)
		r.Value = &v
		r.Display = formatPercent(v)
	}
	return r
}

func formatPercent(v float64) string {
	p := math.Round(v*1000) / 10
	if p == math.Trunc(p) {
		return itoa(int(p)) + "%"
	}
	return ftoa1(p) + "%"
}

func durations(values []float64) DurationStats {
	d := DurationStats{N: len(values), Display: "нет данных"}
	if len(values) == 0 {
		return d
	}
	sort.Float64s(values)
	var median float64
	if n := len(values); n%2 == 1 {
		median = values[n/2]
	} else {
		median = (values[n/2-1] + values[n/2]) / 2
	}
	p95 := values[int(math.Ceil(0.95*float64(len(values))))-1]
	d.MedianSeconds, d.P95Seconds = &median, &p95
	d.Display = "медиана " + humanSeconds(median) + ", p95 " + humanSeconds(p95) + ", N=" + itoa(len(values))
	return d
}

// ComputePilotMetrics recomputes every figure from stored events. from/to are Moscow
// calendar bounds (zero = unbounded); a session belongs to the cohort by the time of its
// first user request. Duplicate event IDs are ignored defensively.
func ComputePilotMetrics(events []Event, from, to time.Time, includeDemo bool) PilotMetrics {
	m := PilotMetrics{TimeZone: "Europe/Moscow (UTC+3)", IncludesDemo: includeDemo,
		StatusCounts:  map[string]int{"new": 0, "in_review": 0, "accepted": 0, "needs_info": 0, "rejected": 0},
		ReturnReasons: map[string]int{}, RejectReasons: map[string]int{}, KnowledgeGaps: map[string]int{},
		Notes: []string{
			"accepted — принятие менеджером в работу, не договор и не выручка",
			"причины потерь показаны только зафиксированные менеджером; причины ухода без заявки не известны",
		}}
	if !from.IsZero() {
		f := from.In(Moscow)
		m.PeriodFrom = &f
	}
	if !to.IsZero() {
		t := to.In(Moscow)
		m.PeriodTo = &t
	}

	seen := map[string]bool{}
	ordered := make([]Event, 0, len(events))
	for _, e := range events {
		if seen[e.ID] {
			continue
		}
		seen[e.ID] = true
		if e.Demo && !includeDemo {
			m.ExcludedDemoEvt++
			continue
		}
		ordered = append(ordered, e)
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].At.Before(ordered[j].At) })

	type sessionFacts struct {
		startedAt    *time.Time
		answered     bool
		firstRequest *time.Time
		firstSaved   *time.Time
	}
	type appFacts struct {
		session       string
		status        ReviewStatus
		statusAt      time.Time
		statusVersion int
		reason        ReasonCode
		firstReview   *time.Time
	}
	sessions := map[string]*sessionFacts{}
	apps := map[string]*appFacts{}
	gaps := []Event{}
	s := func(id string) *sessionFacts {
		if sessions[id] == nil {
			sessions[id] = &sessionFacts{}
		}
		return sessions[id]
	}
	for _, e := range ordered {
		at := e.At
		switch e.Type {
		case EventSessionStarted:
			if f := s(e.SessionID); f.startedAt == nil {
				f.startedAt = &at
			}
		case EventConsultationRequested:
			if f := s(e.SessionID); f.firstRequest == nil {
				f.firstRequest = &at
			}
		case EventFirstAnswerReceived:
			s(e.SessionID).answered = true
		case EventKnowledgeGap:
			gaps = append(gaps, e)
		case EventApplicationSaved, EventReviewStarted, EventAccepted, EventNeedsInfo, EventRejected:
			if e.Type == EventApplicationSaved {
				if f := s(e.SessionID); f.firstSaved == nil {
					f.firstSaved = &at
				}
			}
			a := apps[e.ApplicationID]
			if a == nil {
				a = &appFacts{session: e.SessionID, status: StatusNew}
				apps[e.ApplicationID] = a
			}
			if e.Type == EventApplicationSaved {
				continue // a resave does not change the review status
			}
			if e.Type == EventReviewStarted && a.firstReview == nil {
				a.firstReview = &at
			}
			if e.Version >= a.statusVersion {
				a.statusVersion, a.statusAt, a.reason = e.Version, at, e.Reason
				a.status = map[EventType]ReviewStatus{EventReviewStarted: StatusInReview, EventAccepted: StatusAccepted,
					EventNeedsInfo: StatusNeedsInfo, EventRejected: StatusRejected}[e.Type]
			}
		}
	}

	inPeriod := func(t time.Time) bool {
		return (from.IsZero() || !t.Before(from)) && (to.IsZero() || t.Before(to))
	}
	cohort := map[string]bool{}
	var toApplication []float64
	for id, f := range sessions {
		if f.startedAt != nil && inPeriod(*f.startedAt) {
			m.SessionsStarted++
		}
		if f.firstRequest == nil || !inPeriod(*f.firstRequest) {
			continue
		}
		cohort[id] = true
		m.SessionsWithQuestion++
		if f.answered {
			m.SessionsWithAnswer++
		}
		if f.firstSaved != nil {
			m.SessionsWithApplication++
			if d := f.firstSaved.Sub(*f.firstRequest).Seconds(); d >= 0 {
				toApplication = append(toApplication, d)
			}
		}
	}
	m.Conversion = ratio(m.SessionsWithApplication, m.SessionsWithQuestion)
	m.SessionsWithoutApplication = ratio(m.SessionsWithQuestion-m.SessionsWithApplication, m.SessionsWithQuestion)
	m.TimeToApplication = durations(toApplication)

	var review []float64
	for _, a := range apps {
		if !cohort[a.session] {
			continue
		}
		m.StatusCounts[string(a.status)]++
		switch a.status {
		case StatusNeedsInfo:
			m.ReturnReasons[string(a.reason)]++
		case StatusRejected:
			m.RejectReasons[string(a.reason)]++
		}
		if (a.status == StatusAccepted || a.status == StatusRejected) && a.firstReview != nil {
			if d := a.statusAt.Sub(*a.firstReview).Seconds(); d >= 0 {
				review = append(review, d)
			}
		}
	}
	m.Acceptance = ratio(m.StatusCounts["accepted"], m.StatusCounts["accepted"]+m.StatusCounts["rejected"])
	m.ReviewTime = durations(review)
	for _, g := range gaps {
		if cohort[g.SessionID] {
			m.KnowledgeGaps[g.Topic]++
		}
	}
	return m
}

func itoa(n int) string { return strconv.Itoa(n) }

func ftoa1(v float64) string { return strings.Replace(strconv.FormatFloat(v, 'f', 1, 64), ".", ",", 1) }

func humanSeconds(sec float64) string {
	switch {
	case sec < 60:
		return itoa(int(math.Round(sec))) + " с"
	case sec < 3600:
		return ftoa1(sec/60) + " мин"
	default:
		return ftoa1(sec/3600) + " ч"
	}
}
