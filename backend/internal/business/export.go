package business

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ExportJSON and ExportTXT render the same stored card, so UI and download never diverge.
func ExportJSON(card NeedCard) ([]byte, error) {
	return json.MarshalIndent(card, "", "  ")
}

var moscow = time.FixedZone("MSK", 3*60*60)

func statusLabel(s Status) string {
	switch s {
	case Confirmed:
		return "подтверждено"
	case Assumed:
		return "предположение"
	default:
		return "неизвестно"
	}
}

func fieldLine(label string, f Field) string {
	if f.Status == Unknown || strings.TrimSpace(f.Value) == "" {
		return fmt.Sprintf("%s: — [неизвестно]", label)
	}
	line := fmt.Sprintf("%s: %s [%s]", label, f.Value, statusLabel(f.Status))
	if len(f.Evidence) > 0 {
		refs := make([]string, 0, len(f.Evidence))
		for _, e := range f.Evidence {
			refs = append(refs, fmt.Sprintf("сообщение №%d", e.Sequence))
		}
		line += " (" + strings.Join(refs, ", ") + ")"
	}
	return line
}

func ExportTXT(card NeedCard) string {
	var b strings.Builder
	w := func(format string, args ...interface{}) { fmt.Fprintf(&b, format+"\n", args...) }
	w("КАРТОЧКА ПОТРЕБНОСТИ — МТС Energytool")
	if card.Demo {
		w("ДЕМО-ДАННЫЕ: не реальное обращение клиента")
	}
	number := card.ApplicationNumber
	if number == "" {
		number = card.ApplicationID
	}
	w("Заявка: %s | версия %d | %s МСК", number, card.Version, card.BuiltAt.In(moscow).Format("02.01.2006 15:04"))
	w("Сессия: %s", card.SessionID)
	w("Статус передачи: сохранено в прототипе, в CRM не отправлялось")
	w("")
	w("КОНТАКТ")
	w("%s", fieldLine("Телефон", card.Contact.Phone))
	w("%s", fieldLine("Email", card.Contact.Email))
	w("%s", fieldLine("Имя", card.Contact.FullName))
	w("%s", fieldLine("Компания", card.Contact.Company))
	w("%s", fieldLine("Способ связи", card.Contact.PreferredChannel))
	w("%s", fieldLine("Желаемое время связи", card.ContactTime))
	w("")
	w("ЗАДАЧА КЛИЕНТА")
	w("%s", fieldLine("Задача", card.Task))
	w("")
	w("ИЗВЕСТНЫЕ ПАРАМЕТРЫ (только со слов клиента)")
	found := false
	for _, group := range []struct {
		label  string
		fields []Field
	}{{"Объект", card.Object}, {"Параметр", card.Parameters}, {"Ограничение", card.Constraints}} {
		for _, f := range group.fields {
			w("- %s", fieldLine(group.label, f))
			found = true
		}
	}
	for _, p := range card.Power {
		kind := "тип не указан"
		if p.Kind.Status != Unknown {
			kind = p.Kind.Value
		}
		state := map[PowerState]string{PowerActual: "актуальное", PowerHistory: "прежнее значение, заменено подтверждённым",
			PowerUnconfirmed: "не подтверждено, уточнить"}[p.State]
		w("- Мощность/объём: %s %s (%s; %s)", p.Amount, p.Unit, kind, state)
		found = true
	}
	if !found {
		w("- нет сведений")
	}
	w("")
	w("ОБСУЖДАВШИЕСЯ НАПРАВЛЕНИЯ")
	if len(card.Products) == 0 {
		w("- требует уточнения")
	}
	for _, p := range card.Products {
		w("- %s [%s; основание в базе знаний: %s]", p.Name, statusLabel(p.Status), p.SourceID)
	}
	w("")
	w("ЧТО НЕИЗВЕСТНО / УТОЧНИТЬ")
	if len(card.Unknowns) == 0 {
		w("- нет открытых вопросов")
	}
	for _, u := range card.Unknowns {
		w("- %s", u.Value)
	}
	w("")
	w("СЛЕДУЮЩИЙ ШАГ МЕНЕДЖЕРА")
	w("%s", card.NextStep.Value)
	return b.String()
}
