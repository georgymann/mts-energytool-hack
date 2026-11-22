package business

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"energytool/backend/internal/domain"
)

var t0 = time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)

func idx(i int) *int { return &i }

func msg(id, role, content string, seq int) domain.Message {
	return domain.Message{ID: id, Role: role, Content: content, Sequence: seq, CreatedAt: t0}
}

// fixtureSession: a factory asks about the wholesale market, corrects its power and confirms the form.
func fixtureSession() domain.Session {
	messages := []domain.Message{
		msg("m1", "user", "У нас завод в Тульской области, присоединённая мощность 900 кВт. Хотим на оптовый рынок.", 1),
		msg("m2", "assistant", "При мощности от 750 кВт выход возможен через энергосбытовую компанию.", 2),
		msg("m3", "user", "Извините, перепутал: у нас 600 кВт, а не 900.", 3),
		msg("m4", "assistant", "Ниже 750 кВт выход на оптовый рынок невозможен.", 4),
		msg("m5", "user", "Ещё вопрос: а солнечные панели на крышу цеха вы ставите?", 5),
		msg("m6", "assistant", "Да, это возможно. Оставите контакты?", 6),
	}
	summary := map[string]interface{}{
		"task": []interface{}{map[string]interface{}{"value": "Хотим на оптовый рынок", "message_index": 0}},
		"object": []interface{}{
			map[string]interface{}{"value": "завод в Тульской области", "message_index": 0},
			// Assistant message: must be rejected even though the text exists.
			map[string]interface{}{"value": "энергосбытовую компанию", "message_index": 1},
		},
		"parameters": []interface{}{
			map[string]interface{}{"value": "присоединённая мощность 900 кВт", "message_index": 0},
			map[string]interface{}{"value": "у нас 600 кВт, а не 900", "message_index": 2},
			// Not present in the referenced message: invented by the model, must be dropped.
			map[string]interface{}{"value": "потребление 1 000 000 кВт·ч в месяц", "message_index": 2},
			// Out of range index.
			map[string]interface{}{"value": "600 кВт", "message_index": 99},
		},
		"constraints": []interface{}{},
		"products": []interface{}{
			map[string]interface{}{"name": "Выход на оптовый рынок", "source_id": "20-products/21-orem.md#s003"},
			map[string]interface{}{"name": "Солнечные электростанции", "source_id": "20-products/23-solar.md#s009"},
		},
		"manager_questions": []interface{}{"Какой класс напряжения подключения?"},
	}
	return domain.Session{
		ID: "s1", Status: "active", Messages: messages,
		LastResult: &domain.ConsultationResult{Summary: summary, Status: "answered"},
		Application: &domain.Application{Phone: "+7 900 000-00-00", Summary: "Снизить расходы на электроэнергию завода",
			FullName: "Иван Тестовый", Company: "", SavedAt: t0},
	}
}

func build(t *testing.T, s domain.Session) NeedCard {
	t.Helper()
	card, err := BuildCard(s, "app-1", 1, true, t0)
	if err != nil {
		t.Fatal(err)
	}
	return card
}

func TestCardUsesOnlyVerifiedClientFacts(t *testing.T) {
	card := build(t, fixtureSession())
	if card.Contact.Phone.Value != "+7 900 000-00-00" || card.Contact.Phone.Status != Confirmed {
		t.Fatalf("phone must be exact and confirmed: %+v", card.Contact.Phone)
	}
	if card.Task.Value != "Снизить расходы на электроэнергию завода" || card.Task.Origin != OriginForm {
		t.Fatalf("task must come from the confirmed form: %+v", card.Task)
	}
	if len(card.Object) != 1 || card.Object[0].Evidence[0].MessageID != "m1" {
		t.Fatalf("assistant quote must be rejected, client quote kept with message link: %+v", card.Object)
	}
	for _, p := range card.Parameters {
		if strings.Contains(p.Value, "1 000 000") {
			t.Fatal("quote absent from the referenced message must be dropped")
		}
	}
	if len(card.Parameters) != 2 {
		t.Fatalf("expected 2 verified parameters, got %+v", card.Parameters)
	}
	if card.Version != 1 || card.ApplicationID != "app-1" || card.SessionID != "s1" || !card.Demo {
		t.Fatalf("identity/version/demo flag missing: %+v", card)
	}
}

func TestCorrectionKeepsBothValuesAndAsksToClarify(t *testing.T) {
	card := build(t, fixtureSession())
	if len(card.Power) != 2 {
		t.Fatalf("expected 900 and 600 kW, got %+v", card.Power)
	}
	if card.Power[0].Amount != "900" || card.Power[0].Kind.Value != "присоединённая" {
		t.Fatalf("900 kW was stated as connected power: %+v", card.Power[0])
	}
	if card.Power[1].Amount != "600" || card.Power[1].Kind.Status != Unknown || card.Power[1].Evidence[0].MessageID != "m3" {
		t.Fatalf("600 kW kind was not stated in its sentence: %+v", card.Power[1])
	}
	if !hasUnknown(card, "несколько значений мощности") {
		t.Fatalf("several power values must be flagged: %+v", card.Unknowns)
	}
}

func TestSeveralTopicsStayOneNeed(t *testing.T) {
	card := build(t, fixtureSession())
	if card.Task.Value == "" || len(card.Products) != 2 {
		t.Fatalf("one confirmed task, two discussed directions expected: %+v / %+v", card.Task, card.Products)
	}
	for _, p := range card.Products {
		if p.Status != Assumed || p.SourceID == "" {
			t.Fatalf("consultant products are assumptions with a KB source: %+v", p)
		}
	}
}

func TestPhoneWithoutCompanyIsAccepted(t *testing.T) {
	card := build(t, fixtureSession())
	if card.Contact.Company.Status != Unknown || !hasUnknown(card, "Название компании") {
		t.Fatalf("missing company must be unknown, not invented: %+v", card.Contact.Company)
	}
	if card.Contact.PreferredChannel.Value != "телефон" {
		t.Fatalf("only the phone channel is confirmed: %+v", card.Contact.PreferredChannel)
	}
	if !strings.HasPrefix(card.NextStep.Value, "Позвонить и уточнить: Какой класс напряжения") {
		t.Fatalf("next step should use the consultant's open question: %q", card.NextStep.Value)
	}
}

func TestNoConsultantResultLeavesEverythingUnknown(t *testing.T) {
	s := fixtureSession()
	s.LastResult = nil
	s.Application.Summary = "Нужна консультация"
	card := build(t, s)
	if len(card.Object)+len(card.Parameters)+len(card.Power)+len(card.Products) != 0 {
		t.Fatalf("nothing may be inferred without a source: %+v", card)
	}
	for _, want := range []string{"Тип организации", "Мощность объекта не указана", "требует уточнения"} {
		if !hasUnknown(card, want) {
			t.Fatalf("missing unknown %q in %+v", want, card.Unknowns)
		}
	}
	if !strings.Contains(card.NextStep.Value, "подобрать направление") {
		t.Fatalf("unexpected next step %q", card.NextStep.Value)
	}
	if _, err := BuildCard(domain.Session{ID: "x"}, "a", 1, false, t0); err == nil {
		t.Fatal("card without a saved application must fail")
	}
}

func TestExtractPower(t *testing.T) {
	cases := []struct {
		text, amount, unit, kind string
	}{
		{"присоединённая мощность 600 кВт", "600", "кВт", "присоединённая"},
		{"потребляем 1 000 000 кВт·ч в месяц", "1000000", "кВт·ч", "потребление"},
		{"станция на 2 МВт на земле", "2", "МВт", ""},
		{"около 0,5 кВт", "0.5", "кВт", ""},
	}
	for _, c := range cases {
		got := extractPower(c.text)
		if len(got) != 1 || got[0].Amount != c.amount || got[0].Unit != c.unit || got[0].Kind.Value != c.kind {
			t.Errorf("%q -> %+v", c.text, got)
		}
	}
	if got := extractPower("звонить после 10 утра, 3 склада"); len(got) != 0 {
		t.Errorf("numbers without units are not power: %+v", got)
	}
}

func TestEditsAreVersionedAndAtomic(t *testing.T) {
	card := build(t, fixtureSession())
	if _, _, err := ApplyEdits(card, 0, ActorManager, []Edit{{Path: "task", Action: "set", Value: "x"}}, t0); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale version must conflict, got %v", err)
	}
	// One invalid edit rejects the whole batch.
	bad := []Edit{{Path: "contact.company", Action: "set", Value: "ООО Тест"}, {Path: "contact.phone", Action: "set", Value: "123"}}
	if same, _, err := ApplyEdits(card, 1, ActorClient, bad, t0); !errors.Is(err, ErrInvalidEdit) || same.Contact.Company.Status != Unknown {
		t.Fatalf("batch must be atomic: %v %+v", err, same.Contact.Company)
	}
	edits := []Edit{
		{Path: "contact.company", Action: "set", Value: "ООО Тест"},
		{Path: "power.1.kind", Action: "set", Value: "присоединённая"},
		{Path: "power.0", Action: "remove"},
		{Path: "products.0", Action: "confirm"},
		{Path: "products.1", Action: "remove"},
	}
	next, change, err := ApplyEdits(card, 1, ActorManager, edits, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if next.Version != 2 || change.Version != 2 || len(change.Paths) != 5 || card.Version != 1 {
		t.Fatalf("version must grow by one and the old card stay intact: %d %+v", next.Version, change)
	}
	if next.Contact.Company.Origin != OriginManager || hasUnknown(next, "Название компании") {
		t.Fatalf("company unknown must disappear after confirmation: %+v", next.Unknowns)
	}
	if len(next.Power) != 1 || next.Power[0].Amount != "600" || hasUnknown(next, "несколько значений") || hasUnknown(next, "Тип мощности") {
		t.Fatalf("correction resolved: %+v / %+v", next.Power, next.Unknowns)
	}
	if len(next.Products) != 1 || next.Products[0].Status != Confirmed {
		t.Fatalf("product confirmed/removed: %+v", next.Products)
	}
	if !hasUnknown(next, "класс напряжения") {
		t.Fatal("consultant question must survive edits until resolved")
	}
	if len(card.Power) != 2 || card.Products[0].Status != Assumed {
		t.Fatal("previous version was mutated")
	}
	if strings.Contains(strings.Join(change.Paths, " "), "ООО") {
		t.Fatal("change log must not contain values")
	}
}

func TestManualNextStepIsKept(t *testing.T) {
	card := build(t, fixtureSession())
	next, _, err := ApplyEdits(card, 1, ActorManager, []Edit{{Path: "next_step", Action: "set", Value: "Отправить опросный лист"}}, t0)
	if err != nil {
		t.Fatal(err)
	}
	next, _, err = ApplyEdits(next, 2, ActorManager, []Edit{{Path: "contact.company", Action: "set", Value: "ООО Тест"}}, t0)
	if err != nil || next.NextStep.Value != "Отправить опросный лист" {
		t.Fatalf("manager's next step must not be overwritten: %v %+v", err, next.NextStep)
	}
}

func TestExportsRenderTheSameCard(t *testing.T) {
	card := build(t, fixtureSession())
	raw, err := ExportJSON(card)
	if err != nil {
		t.Fatal(err)
	}
	var back NeedCard
	if err := json.Unmarshal(raw, &back); err != nil || back.Version != card.Version || back.Contact.Phone.Value != card.Contact.Phone.Value {
		t.Fatalf("JSON export must round-trip: %v", err)
	}
	txt := ExportTXT(card)
	for _, want := range []string{"+7 900 000-00-00", "ДЕМО-ДАННЫЕ", "Компания: — [неизвестно]", "сообщение №1",
		"600 кВт (тип не указан; не подтверждено, уточнить)", "Выход на оптовый рынок [предположение", "в CRM не отправлялось", "12:00 МСК"} {
		if !strings.Contains(txt, want) {
			t.Errorf("TXT export misses %q:\n%s", want, txt)
		}
	}
}

func hasUnknown(card NeedCard, part string) bool {
	for _, u := range card.Unknowns {
		if strings.Contains(u.Value, part) {
			return true
		}
	}
	return false
}

// Team-lead rule: a value the client confirmed in the form is the one in force;
// the earlier chat value stays as history; two values are never actual together.
func TestFormConfirmedPowerWinsAndKeepsHistory(t *testing.T) {
	s := fixtureSession()
	s.Application.Summary = "Снизить расходы завода, присоединённая мощность 600 кВт"
	card := build(t, s)
	states := map[string]PowerState{}
	for _, p := range card.Power {
		states[p.Amount] = p.State
	}
	if states["600"] != PowerActual || states["900"] != PowerHistory || len(card.Power) != 2 {
		t.Fatalf("600 from the form must be actual, 900 history: %+v", card.Power)
	}
	if hasUnknown(card, "несколько значений") {
		t.Fatal("a confirmed value resolves the conflict")
	}
	for _, p := range card.Power {
		if p.Amount == "600" && (p.Origin != OriginForm || len(p.Evidence) == 0 || p.Kind.Value != "присоединённая") {
			t.Fatalf("form value keeps the chat evidence and its stated kind: %+v", p)
		}
	}
	if !strings.Contains(ExportTXT(card), "900 кВт (присоединённая; прежнее значение, заменено подтверждённым)") {
		t.Fatalf("history must be visible in the export:\n%s", ExportTXT(card))
	}
}

func TestConfirmEditResolvesConflict(t *testing.T) {
	card := build(t, fixtureSession())
	if card.Power[0].State != PowerUnconfirmed || card.Power[1].State != PowerUnconfirmed {
		t.Fatalf("without confirmation both values are unconfirmed: %+v", card.Power)
	}
	next, _, err := ApplyEdits(card, 1, ActorClient, []Edit{{Path: "power.1", Action: "confirm"}}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if next.Power[1].State != PowerActual || next.Power[0].State != PowerHistory || hasUnknown(next, "несколько значений") {
		t.Fatalf("confirmed 600 actual, 900 history: %+v", next.Power)
	}
	actual := 0
	for _, p := range next.Power {
		if p.State == PowerActual {
			actual++
		}
	}
	if actual != 1 {
		t.Fatal("only one power value may be actual")
	}
}
