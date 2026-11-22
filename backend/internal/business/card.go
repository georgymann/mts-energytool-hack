// Package business holds presale business rules: the need card handed to a
// manager, its edits and versions. It has no HTTP or storage code; the core API
// owner wires it into routes and persistence.
//
// Rules of the card:
//   - every value says where it came from and whether the client confirmed it;
//   - nothing is inferred beyond what the client wrote or the consultant result
//     already returned (no new LLM call, no eligibility scoring, no savings);
//   - a quote is accepted only if it is found verbatim in a client message.
package business

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"energytool/backend/internal/domain"
)

const CardSchemaVersion = "1.0"

type Status string

const (
	Confirmed Status = "confirmed" // stated or confirmed by the client (or a manager edit)
	Assumed   Status = "assumed"   // proposed by the consultant, not yet confirmed
	Unknown   Status = "unknown"   // not known; listed for the manager to clarify
)

type Origin string

const (
	OriginForm       Origin = "application_form" // client typed it into the confirmation form
	OriginMessage    Origin = "client_message"   // verbatim quote of a client chat message
	OriginConsultant Origin = "consultant"       // consultant result (products, manager questions)
	OriginRule       Origin = "rule"             // deterministic card rule
	OriginClientEdit Origin = "client_edit"
	OriginManager    Origin = "manager_edit"
)

type Evidence struct {
	MessageID string `json:"message_id"`
	Sequence  int    `json:"sequence"`
	Quote     string `json:"quote"`
}

type Field struct {
	Value    string     `json:"value"`
	Status   Status     `json:"status"`
	Origin   Origin     `json:"origin"`
	Evidence []Evidence `json:"evidence,omitempty"`
}

type Contact struct {
	Phone            Field `json:"phone"`
	Email            Field `json:"email"`
	FullName         Field `json:"full_name"`
	Company          Field `json:"company"`
	PreferredChannel Field `json:"preferred_channel"`
}

// PowerState says which value is in force when the client named several.
type PowerState string

const (
	PowerActual      PowerState = "actual"      // the value in force
	PowerHistory     PowerState = "history"     // replaced by a value the client confirmed
	PowerUnconfirmed PowerState = "unconfirmed" // several values, none confirmed yet: ask the client
)

// Power is recorded only when the client wrote a number with a unit.
// A value from the confirmed form or an explicit edit outranks values from chat messages.
type Power struct {
	Amount              string     `json:"amount"`
	Unit                string     `json:"unit"` // кВт, МВт or кВт·ч
	Kind                Field      `json:"kind"` // присоединённая / потребление, unknown if not stated
	State               PowerState `json:"state"`
	Origin              Origin     `json:"origin"`                         // application_form | client_message | client_edit | manager_edit
	ConfirmationVersion int        `json:"confirmation_version,omitempty"` // explicit edit supersedes older confirmation
	Evidence            []Evidence `json:"evidence"`
}

type Product struct {
	Name     string `json:"name"`
	SourceID string `json:"source_id"` // knowledge-base fragment the consultant relied on
	Status   Status `json:"status"`
	Origin   Origin `json:"origin"`
}

type NeedCard struct {
	ContactTime       Field     `json:"contact_time"`
	SchemaVersion     string    `json:"schema_version"`
	ApplicationID     string    `json:"application_id"`
	ApplicationNumber string    `json:"application_number,omitempty"`
	SessionID         string    `json:"session_id"`
	Version           int       `json:"version"`
	BuiltAt           time.Time `json:"built_at"`
	Demo              bool      `json:"demo"`
	Contact           Contact   `json:"contact"`
	Task              Field     `json:"task"`
	Object            []Field   `json:"object"`
	Parameters        []Field   `json:"parameters"`
	Constraints       []Field   `json:"constraints"`
	Power             []Power   `json:"power"`
	Products          []Product `json:"products"`
	Unknowns          []Field   `json:"unknowns"`
	NextStep          Field     `json:"next_step"`
}

// consultantSummary mirrors make_summary in agent/consultant.py.
type consultantSummary struct {
	Task        []quoted `json:"task"`
	Object      []quoted `json:"object"`
	Parameters  []quoted `json:"parameters"`
	Constraints []quoted `json:"constraints"`
	Products    []struct {
		Name     string `json:"name"`
		SourceID string `json:"source_id"`
	} `json:"products"`
	ManagerQuestions []string `json:"manager_questions"`
}

type quoted struct {
	Value        string `json:"value"`
	MessageIndex *int   `json:"message_index"`
}

// BuildCard assembles the card from a session with a saved application.
// applicationID/version are assigned by the caller (application record).
func BuildCard(session domain.Session, applicationID string, version int, demo bool, now time.Time) (NeedCard, error) {
	app := session.Application
	if app == nil {
		return NeedCard{}, fmt.Errorf("session %s has no saved application", session.ID)
	}
	card := NeedCard{SchemaVersion: CardSchemaVersion, ApplicationID: applicationID, ApplicationNumber: app.Number, SessionID: session.ID,
		Version: version, BuiltAt: now.UTC(), Demo: demo,
		Object: []Field{}, Parameters: []Field{}, Constraints: []Field{}, Power: []Power{},
		Products: []Product{}, Unknowns: []Field{}}

	card.Contact = Contact{
		Phone:    formField(app.Phone),
		Email:    formField(app.Email),
		FullName: formField(app.FullName),
		Company:  formField(app.Company),
	}
	card.Contact.PreferredChannel = preferredChannel(card.Contact)
	card.Task = formField(FormTask(app.Summary))
	card.ContactTime = formField(app.ContactPreference())
	if strings.TrimSpace(app.Phone) == "" {
		// Callback preferences belong to a phone contact. The email-only form
		// does not ask for a response time, even if legacy data carries "asap".
		card.ContactTime = formField("")
	}

	summary := decodeSummary(session.LastResult)
	card.Object = verifiedFields(summary.Object, session.Messages)
	card.Parameters = verifiedFields(summary.Parameters, session.Messages)
	card.Constraints = verifiedFields(summary.Constraints, session.Messages)
	if card.Task.Status == Unknown {
		// The form requires a task, but keep a defensive fallback to the client's own words.
		if facts := verifiedFields(summary.Task, session.Messages); len(facts) > 0 {
			card.Task = facts[0]
		}
	}

	// Power: numbers with units in client-confirmed text only (form task + verified quotes).
	for _, f := range append(append([]Field{card.Task}, card.Parameters...), card.Object...) {
		if f.Status != Confirmed {
			continue
		}
		for _, p := range extractPower(f.Value) {
			p.Evidence, p.Origin = f.Evidence, f.Origin
			card.Power = appendPower(card.Power, p)
		}
	}
	resolvePower(card.Power)

	seenProduct := map[string]bool{}
	for _, p := range summary.Products {
		name := strings.TrimSpace(p.Name)
		if name == "" || seenProduct[strings.ToLower(name)] {
			continue
		}
		seenProduct[strings.ToLower(name)] = true
		card.Products = append(card.Products, Product{Name: name, SourceID: p.SourceID, Status: Assumed, Origin: OriginConsultant})
	}

	card.Unknowns = unknowns(card, summary.ManagerQuestions)
	card.NextStep = nextStep(card)
	return card, nil
}

func formField(value string) Field {
	value = strings.TrimSpace(value)
	if value == "" {
		return Field{Status: Unknown, Origin: OriginForm}
	}
	return Field{Value: value, Status: Confirmed, Origin: OriginForm}
}

func decodeSummary(result *domain.ConsultationResult) consultantSummary {
	var s consultantSummary
	if result == nil || result.Summary == nil {
		return s
	}
	raw, err := json.Marshal(result.Summary)
	if err != nil {
		return consultantSummary{}
	}
	if json.Unmarshal(raw, &s) != nil {
		return consultantSummary{}
	}
	return s
}

// verifiedFields keeps only quotes found verbatim in the referenced client message.
func verifiedFields(items []quoted, messages []domain.Message) []Field {
	out := []Field{}
	seen := map[string]bool{}
	for _, item := range items {
		quote := strings.TrimSpace(item.Value)
		if quote == "" || item.MessageIndex == nil || utf8.RuneCountInString(quote) > 500 {
			continue
		}
		i := *item.MessageIndex
		if i < 0 || i >= len(messages) || messages[i].Role != "user" {
			continue
		}
		if !strings.Contains(messages[i].Content, quote) {
			continue
		}
		key := normalize(quote)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Field{Value: quote, Status: Confirmed, Origin: OriginMessage,
			Evidence: []Evidence{{MessageID: messages[i].ID, Sequence: messages[i].Sequence, Quote: quote}}})
	}
	return out
}

func normalize(s string) string {
	s = strings.ToLower(strings.ReplaceAll(s, "ё", "е"))
	return strings.Join(strings.Fields(s), " ")
}

func unknowns(card NeedCard, managerQuestions []string) []Field {
	out := []Field{}
	add := func(text string, origin Origin) {
		for _, u := range out {
			if u.Value == text {
				return
			}
		}
		out = append(out, Field{Value: text, Status: Unknown, Origin: origin})
	}
	if len(card.Object) == 0 {
		add("Тип организации и объекта не указан", OriginRule)
	}
	if len(card.Power) == 0 {
		add("Мощность объекта не указана", OriginRule)
	} else {
		conflict := false
		for _, p := range card.Power {
			if p.State == PowerHistory {
				continue
			}
			conflict = conflict || p.State == PowerUnconfirmed
			if p.Kind.Status == Unknown && p.Unit != "кВт·ч" {
				add(fmt.Sprintf("Тип мощности %s %s не указан: присоединённая или фактическая", p.Amount, p.Unit), OriginRule)
			}
		}
		if conflict {
			add("Клиент назвал несколько значений мощности — уточнить актуальное", OriginRule)
		}
	}
	if len(card.Products) == 0 {
		add("Направление/продукт требует уточнения", OriginRule)
	}
	if card.Contact.FullName.Status == Unknown {
		add("Имя контактного лица не указано", OriginRule)
	}
	if card.Contact.Company.Status == Unknown {
		add("Название компании не указано", OriginRule)
	}
	for _, q := range managerQuestions {
		if q = strings.TrimSpace(q); q != "" {
			add(q, OriginConsultant)
		}
	}
	return out
}

// Prefer the supplied phone when both contact methods are available.
func preferredChannel(contact Contact) Field {
	if contact.Phone.Value != "" {
		return Field{Value: "телефон", Status: Confirmed, Origin: contact.Phone.Origin}
	}
	if contact.Email.Value != "" {
		return Field{Value: "email", Status: Confirmed, Origin: contact.Email.Origin}
	}
	return Field{Status: Unknown, Origin: OriginRule}
}

func nextStep(card NeedCard) Field {
	action := "Позвонить"
	if card.Contact.Phone.Value == "" {
		action = "Связаться"
		if card.Contact.Email.Value != "" {
			action = "Написать по электронной почте"
		}
	}
	for _, u := range card.Unknowns {
		if u.Origin == OriginConsultant {
			return Field{Value: action + " и уточнить: " + u.Value, Status: Assumed, Origin: OriginRule}
		}
	}
	if len(card.Products) == 0 {
		return Field{Value: action + ", уточнить задачу и подобрать направление", Status: Assumed, Origin: OriginRule}
	}
	if len(card.Unknowns) > 0 {
		return Field{Value: action + " и уточнить: " + card.Unknowns[0].Value, Status: Assumed, Origin: OriginRule}
	}
	return Field{Value: action + ", подтвердить задачу и параметры, обсудить предварительную оценку", Status: Assumed, Origin: OriginRule}
}

// FormTask keeps the client's complete review text in Application.Summary while
// extracting the task section from the chat's explicitly labelled review format.
// Free-form client edits remain unchanged.
func FormTask(summary string) string {
	value := strings.TrimSpace(strings.ReplaceAll(summary, "\r\n", "\n"))
	rest, ok := strings.CutPrefix(value, "Задача клиента:\n")
	if !ok {
		return value
	}
	task, _, _ := strings.Cut(rest, "\n\n")
	lines := strings.Split(task, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "•"))
	}
	task = strings.TrimSpace(strings.Join(lines, "\n"))
	if task == "" {
		return value
	}
	return task
}
