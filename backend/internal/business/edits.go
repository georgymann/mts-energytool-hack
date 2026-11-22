package business

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"energytool/backend/internal/domain"
)

var (
	ErrVersionConflict = errors.New("card version is stale")
	ErrInvalidEdit     = errors.New("invalid card edit")
)

type Actor string

const (
	ActorClient  Actor = "client"
	ActorManager Actor = "manager"
)

// Edit is one change to the card. Supported paths and actions:
//
//	task, next_step                         set
//	contact.phone|email|full_name|company   set, remove (retain at least one contact)
//	power                                   add (value like "600 кВт")
//	power.N                                 confirm (becomes actual), remove
//	power.N.kind                            set ("присоединённая" | "потребление")
//	products.N                              confirm, remove
//	unknowns                                add
//	unknowns.N                              remove (resolved)
type Edit struct {
	Path   string `json:"path"`
	Action string `json:"action"`
	Value  string `json:"value,omitempty"`
}

// Change is the audit entry of one accepted edit batch. It lists paths, not values,
// so it can be shown or logged without contact data.
type Change struct {
	Version int       `json:"version"`
	At      time.Time `json:"at"`
	Actor   Actor     `json:"actor"`
	Paths   []string  `json:"paths"`
}

// ApplyEdits validates all edits first and applies them atomically: either every
// edit is applied and the version grows by one, or the card is returned unchanged.
func ApplyEdits(card NeedCard, expectedVersion int, actor Actor, edits []Edit, now time.Time) (NeedCard, Change, error) {
	if expectedVersion != card.Version {
		return card, Change{}, ErrVersionConflict
	}
	if actor != ActorClient && actor != ActorManager {
		return card, Change{}, fmt.Errorf("%w: unknown actor", ErrInvalidEdit)
	}
	if len(edits) == 0 || len(edits) > 50 {
		return card, Change{}, fmt.Errorf("%w: 1..50 edits expected", ErrInvalidEdit)
	}
	next := cloneCard(card)
	origin := OriginClientEdit
	if actor == ActorManager {
		origin = OriginManager
	}
	nextStepSet := false
	removals := map[string][]int{} // applied after the rest so indexes stay stable
	paths := []string{}
	for _, e := range edits {
		value := strings.TrimSpace(e.Value)
		if utf8.RuneCountInString(value) > 2000 {
			return card, Change{}, fmt.Errorf("%w: value too long", ErrInvalidEdit)
		}
		parts := strings.Split(e.Path, ".")
		idx := -1
		if len(parts) >= 2 {
			if n, err := strconv.Atoi(parts[1]); err == nil {
				idx = n
			}
		}
		set := func(f *Field) error {
			if e.Action != "set" || value == "" {
				return fmt.Errorf("%w: %s needs action=set and a value", ErrInvalidEdit, e.Path)
			}
			*f = Field{Value: value, Status: Confirmed, Origin: origin}
			return nil
		}
		var err error
		switch {
		case e.Path == "task":
			err = set(&next.Task)
			if err == nil {
				for _, p := range extractPower(value) {
					p.Origin, p.ConfirmationVersion = origin, card.Version+1
					next.Power = appendPower(next.Power, p)
				}
			}
		case e.Path == "next_step":
			err = set(&next.NextStep)
			nextStepSet = true
		case e.Path == "contact.phone" || e.Path == "contact.email":
			f, valid := &next.Contact.Phone, domain.ValidPhone
			if e.Path == "contact.email" {
				f, valid = &next.Contact.Email, domain.ValidEmail
			}
			if e.Action == "remove" {
				*f = Field{Status: Unknown, Origin: origin}
			} else if !valid(value) {
				err = fmt.Errorf("%w: invalid %s", ErrInvalidEdit, e.Path)
			} else {
				err = set(f)
			}
		case e.Path == "contact.full_name" || e.Path == "contact.company":
			f := &next.Contact.FullName
			if e.Path == "contact.company" {
				f = &next.Contact.Company
			}
			if e.Action == "remove" {
				*f = Field{Status: Unknown, Origin: origin}
			} else {
				err = set(f)
			}
		case e.Path == "power" && e.Action == "add":
			found := extractPower(value)
			if len(found) != 1 {
				err = fmt.Errorf("%w: power must be one number with unit (кВт, МВт, кВт·ч)", ErrInvalidEdit)
			} else {
				p := found[0]
				p.Origin = origin
				p.ConfirmationVersion = card.Version + 1
				if p.Kind.Status == Confirmed {
					p.Kind.Origin = origin
				}
				next.Power = appendPower(next.Power, p)
			}
		case len(parts) == 3 && parts[0] == "power" && parts[2] == "kind" && idx >= 0 && idx < len(next.Power):
			if e.Action != "set" || (value != "присоединённая" && value != "потребление") {
				err = fmt.Errorf("%w: kind is присоединённая or потребление", ErrInvalidEdit)
			} else {
				next.Power[idx].Kind = Field{Value: value, Status: Confirmed, Origin: origin}
			}
		case len(parts) == 2 && parts[0] == "power" && idx >= 0 && idx < len(next.Power) && e.Action == "remove":
			removals["power"] = append(removals["power"], idx)
		case len(parts) == 2 && parts[0] == "power" && idx >= 0 && idx < len(next.Power) && e.Action == "confirm":
			next.Power[idx].Origin = origin
			next.Power[idx].ConfirmationVersion = card.Version + 1
		case len(parts) == 2 && parts[0] == "products" && idx >= 0 && idx < len(next.Products):
			switch e.Action {
			case "confirm":
				next.Products[idx].Status, next.Products[idx].Origin = Confirmed, origin
			case "remove":
				removals["products"] = append(removals["products"], idx)
			default:
				err = fmt.Errorf("%w: products.N supports confirm/remove", ErrInvalidEdit)
			}
		case e.Path == "unknowns" && e.Action == "add" && value != "":
			next.Unknowns = append(next.Unknowns, Field{Value: value, Status: Unknown, Origin: origin})
		case len(parts) == 2 && parts[0] == "unknowns" && idx >= 0 && idx < len(next.Unknowns) && e.Action == "remove":
			removals["unknowns"] = append(removals["unknowns"], idx)
		default:
			err = fmt.Errorf("%w: unsupported path/action %s/%s", ErrInvalidEdit, e.Path, e.Action)
		}
		if err != nil {
			return card, Change{}, err
		}
		paths = append(paths, e.Path)
	}
	if !domain.ValidContacts(next.Contact.Phone.Value, next.Contact.Email.Value) {
		return card, Change{}, fmt.Errorf("%w: at least one valid contact is required", ErrInvalidEdit)
	}
	next.Contact.PreferredChannel = preferredChannel(next.Contact)
	if card.Contact.Phone.Value != "" && next.Contact.Phone.Value == "" && card.ContactTime.Origin == OriginForm {
		next.ContactTime = Field{Status: Unknown, Origin: origin}
	}

	next.Power = removeIndexes(next.Power, removals["power"])
	resolvePower(next.Power)
	next.Products = removeIndexes(next.Products, removals["products"])
	next.Unknowns = removeIndexes(next.Unknowns, removals["unknowns"])

	// Rule-based unknowns follow the edited facts; human/consultant entries stay as they are.
	kept := []Field{}
	for _, u := range next.Unknowns {
		if u.Origin != OriginRule {
			kept = append(kept, u)
		}
	}
	next.Unknowns = append(unknowns(next, nil), kept...)
	if !nextStepSet && next.NextStep.Origin == OriginRule {
		next.NextStep = nextStep(next)
	}
	next.Version = card.Version + 1
	next.BuiltAt = now.UTC()
	return next, Change{Version: next.Version, At: now.UTC(), Actor: actor, Paths: paths}, nil
}

func removeIndexes[T any](items []T, indexes []int) []T {
	if len(indexes) == 0 {
		return items
	}
	drop := map[int]bool{}
	for _, i := range indexes {
		drop[i] = true
	}
	out := make([]T, 0, len(items))
	for i, item := range items {
		if !drop[i] {
			out = append(out, item)
		}
	}
	return out
}

func cloneCard(c NeedCard) NeedCard {
	out := c
	out.Object = append([]Field{}, c.Object...)
	out.Parameters = append([]Field{}, c.Parameters...)
	out.Constraints = append([]Field{}, c.Constraints...)
	out.Power = append([]Power{}, c.Power...)
	for i := range out.Power {
		out.Power[i].Evidence = append([]Evidence{}, c.Power[i].Evidence...)
	}
	out.Products = append([]Product{}, c.Products...)
	out.Unknowns = append([]Field{}, c.Unknowns...)
	return out
}
