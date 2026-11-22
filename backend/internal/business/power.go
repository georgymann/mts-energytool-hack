package business

import (
	"regexp"
	"strings"
)

// A number followed by a power/energy unit, as the client wrote it.
// кВт·ч / кВтч / кВт*ч / кВт ч are energy (consumption), not power.
// Thousands may be grouped with spaces: «1 000 000 кВт·ч».
var powerPattern = regexp.MustCompile(`(?i)(\d{1,3}(?:[ \x{00A0}]\d{3})+|\d+(?:[.,]\d+)?)\s*(квт\s*[·*.]?\s*ч|квтч|мвт|квт)(?:[^а-яё]|$)`)

// Kind words are taken only from the same client sentence; otherwise the kind stays unknown.
var (
	connectedWords   = regexp.MustCompile(`(?i)присоедин|разрешенн|разрешённ|максимальн`)
	consumptionWords = regexp.MustCompile(`(?i)в месяц|в год|в сутки|потребля|потреблени|расход`)
)

func extractPower(text string) []Power {
	out := []Power{}
	lower := strings.ToLower(strings.ReplaceAll(text, "ё", "е"))
	matches := powerPattern.FindAllStringSubmatchIndex(lower, -1)
	for n, indexes := range matches {
		m := []string{lower[indexes[0]:indexes[1]], lower[indexes[2]:indexes[3]], lower[indexes[4]:indexes[5]]}
		// Limit kind words to this measurement's clause, not the entire summary.
		left, right := 0, len(lower)
		if n > 0 {
			left = matches[n-1][5]
		}
		if n+1 < len(matches) {
			right = matches[n+1][0]
		}
		if cut := strings.LastIndexAny(lower[left:indexes[0]], ".!?;,\n"); cut >= 0 {
			left += cut + 1
		}
		if cut := strings.IndexAny(lower[indexes[5]:right], ".!?;,\n"); cut >= 0 {
			right = indexes[5] + cut
		}
		local := lower[left:right]
		amount := strings.NewReplacer(",", ".", " ", "", " ", "").Replace(m[1])
		unit := strings.ReplaceAll(m[2], " ", "")
		p := Power{Amount: amount}
		switch {
		case strings.HasPrefix(unit, "мвт"):
			p.Unit = "МВт"
		case strings.HasSuffix(unit, "ч"):
			p.Unit = "кВт·ч"
		default:
			p.Unit = "кВт"
		}
		switch {
		case p.Unit == "кВт·ч":
			p.Kind = Field{Value: "потребление", Status: Confirmed, Origin: OriginRule}
		case connectedWords.MatchString(local) && !consumptionWords.MatchString(local):
			p.Kind = Field{Value: "присоединённая", Status: Confirmed, Origin: OriginRule}
		case consumptionWords.MatchString(local) && !connectedWords.MatchString(local):
			p.Kind = Field{Value: "потребление", Status: Confirmed, Origin: OriginRule}
		default:
			p.Kind = Field{Status: Unknown, Origin: OriginRule}
		}
		out = append(out, p)
	}
	return out
}

func authoritative(o Origin) bool {
	return o == OriginForm || o == OriginClientEdit || o == OriginManager
}

func powerClass(unit string) string {
	if unit == "кВт·ч" {
		return "energy"
	}
	return "power"
}

// Explicit newer confirmations win over older form/chat values. Different known
// kinds (connected vs consumed power) remain separate measurements. Ambiguous
// values of equal priority require clarification instead of guessing a correction.
func resolvePower(list []Power) {
	priority := func(p Power) int {
		if authoritative(p.Origin) {
			return p.ConfirmationVersion + 1
		}
		return 0
	}
	for i, p := range list {
		best, peers := priority(p), 1
		for j, other := range list {
			if i == j || powerClass(p.Unit) != powerClass(other.Unit) {
				continue
			}
			if p.Kind.Status != Unknown && other.Kind.Status != Unknown && p.Kind.Value != other.Kind.Value {
				continue
			}
			if priority(other) > best {
				best, peers = priority(other), 1
			} else if priority(other) == best {
				peers++
			}
		}
		switch {
		case priority(p) < best:
			list[i].State = PowerHistory
		case peers > 1:
			list[i].State = PowerUnconfirmed
		default:
			list[i].State = PowerActual
		}
	}
}

// appendPower merges repeated mentions of the same value and unit, keeping all evidence,
// a stated kind over an unknown one and the most authoritative origin.
func appendPower(list []Power, p Power) []Power {
	for i := range list {
		if list[i].Amount == p.Amount && list[i].Unit == p.Unit &&
			(list[i].Kind.Status == Unknown || p.Kind.Status == Unknown || list[i].Kind.Value == p.Kind.Value) {
			if list[i].Kind.Status == Unknown && p.Kind.Status != Unknown {
				list[i].Kind = p.Kind
			}
			if authoritative(p.Origin) && (!authoritative(list[i].Origin) || p.ConfirmationVersion > list[i].ConfirmationVersion) {
				list[i].Origin = p.Origin
				list[i].ConfirmationVersion = p.ConfirmationVersion
			}
			for _, e := range p.Evidence {
				dup := false
				for _, x := range list[i].Evidence {
					dup = dup || x.MessageID == e.MessageID
				}
				if !dup {
					list[i].Evidence = append(list[i].Evidence, e)
				}
			}
			return list
		}
	}
	if p.Evidence == nil {
		p.Evidence = []Evidence{}
	}
	return append(list, p)
}
