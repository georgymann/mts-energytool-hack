package domain

import (
	"strings"
	"testing"
)

func TestValidEmailBoundsAndSingleMailbox(t *testing.T) {
	longest := strings.Repeat("a", 64) + "@" + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	for _, email := range []string{"Client.Name+meter@example.org", "energy@sub-domain.example.ru", "o'connor@example.org", longest} {
		if !ValidEmail(email) {
			t.Errorf("valid mailbox rejected: %q", email)
		}
	}
	for _, email := range []string{
		"", "not-an-email", "name@example", "@example.org", "name@.org", "name@example..org", "name@-example.org", "name@example-.org",
		"name@exam_ple.org", "Name <name@example.org>", "name@example.org,other@example.org", "name@example.org (Name)",
		" name@example.org", "name@example.org ", "name @example.org", "na me@example.org", "name@example.org\r\nBcc: other@example.org",
		".name@example.org", "name.@example.org", "na..me@example.org", "\"name\"@example.org", "имя@example.org", "name@пример.рф",
		strings.Repeat("a", 65) + "@example.org", "a@" + strings.Repeat("b", 64) + ".org", longest + "d",
	} {
		if ValidEmail(email) {
			t.Errorf("invalid mailbox accepted: %q", email)
		}
	}
}

func TestValidContactsRequiresOneAndValidatesBoth(t *testing.T) {
	for _, tc := range []struct {
		phone, email string
		want         bool
	}{
		{"+7 (999) 123-45-67", "", true}, {"", "name@example.org", true}, {"+7 999 123 45 67", "name@example.org", true},
		{"", "", false}, {"123", "name@example.org", false}, {"+7 999 123 45 67", "broken", false},
		{strings.Repeat(" ", 30) + "79991234567", "", false}, {"7999123456789012", "", false},
	} {
		if got := ValidContacts(tc.phone, tc.email); got != tc.want {
			t.Errorf("contacts(%q, %q) = %v, want %v", tc.phone, tc.email, got, tc.want)
		}
	}
}
