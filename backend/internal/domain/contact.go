package domain

import (
	"net/mail"
	"regexp"
	"strings"
)

var phoneChars = regexp.MustCompile(`^\+?[0-9 ()-]+$`)

// ValidPhone keeps the reviewed form's existing phone syntax and size limits.
func ValidPhone(phone string) bool {
	if len(phone) > 40 || !phoneChars.MatchString(phone) {
		return false
	}
	digits := 0
	for _, c := range phone {
		if c >= '0' && c <= '9' {
			digits++
		}
	}
	return digits >= 10 && digits <= 15
}

// ValidEmail accepts one bounded ASCII mailbox, not a display name or address list.
// It checks syntax only; no mail is sent and no DNS lookup is performed.
func ValidEmail(email string) bool {
	if email == "" || len(email) > 254 {
		return false
	}
	address, err := mail.ParseAddress(email)
	if err != nil || address.Name != "" || address.Address != email {
		return false
	}
	local, host, ok := strings.Cut(email, "@")
	if !ok || len(local) > 64 || local == "" {
		return false
	}
	for _, c := range local {
		if !asciiAlnum(c) && !strings.ContainsRune("!#$%&'*+-/=?^_`{|}~.", c) {
			return false
		}
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !asciiAlnum(c) && c != '-' {
				return false
			}
		}
	}
	return true
}

func asciiAlnum(c rune) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// ValidContacts requires at least one contact and validates each supplied value.
func ValidContacts(phone, email string) bool {
	return (phone != "" || email != "") && (phone == "" || ValidPhone(phone)) && (email == "" || ValidEmail(email))
}
