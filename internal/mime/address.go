package mime

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/net/idna"
)

const (
	// maxAddressBytes is the longest mail address (RFC 5321: 254 bytes).
	maxAddressBytes = 254
	// maxLocalPartBytes is the longest part before the @ (RFC 5321: 64 bytes).
	maxLocalPartBytes = 64
	// maxLabelBytes is the longest part of a domain name (RFC 1035: 63 bytes).
	maxLabelBytes = 63
	// firstPrintable and lastPrintable are the ends of printable ASCII, without
	// the space. An address has no space.
	firstPrintable, lastPrintable = '!', '~'
)

// atomMarks are the characters that an atom of a local part may hold, next to
// letters and digits (RFC 5322, atext).
const atomMarks = "!#$%&'*+-/=?^_`{|}~"

// ErrNonASCIILocalPart means that a caller gave a recipient with characters
// outside ASCII before the @. Such a local part has no ASCII form to show next
// to it, so a person cannot tell it from a look-alike. An address that a reply
// takes from the original is not refused (see resolveRecipients). It matches
// ErrInvalidAddress.
var ErrNonASCIILocalPart = fmt.Errorf("%w: the part before the @ has characters outside ASCII, so the user could not tell it from a look-alike", ErrInvalidAddress)

// splitAddress returns the parts before and after the last @. An address
// without an @ has only a local part.
func splitAddress(addr string) (local, domain string) {
	if i := strings.LastIndexByte(addr, '@'); i >= 0 {
		return addr[:i], addr[i+1:]
	}
	return addr, ""
}

// isPrintableASCII reports whether every byte of s is printable ASCII.
func isPrintableASCII(s string) bool {
	for i := range len(s) {
		if s[i] < firstPrintable || s[i] > lastPrintable {
			return false
		}
	}
	return true
}

// HasNonASCIIAddress reports whether an address has a character outside
// printable ASCII. Such an address can look like another one, so the service
// always asks the user before it sends to it.
func HasNonASCIIAddress(list []string) bool {
	return slices.ContainsFunc(list, func(addr string) bool { return !isPrintableASCII(addr) })
}

// HasASCIIDomain reports whether the domain of addr can be written in ASCII. A
// domain that cannot has no form to show next to it, so a person could not see
// where the message goes.
func HasASCIIDomain(addr string) bool {
	_, domain := splitAddress(addr)
	if isPrintableASCII(domain) {
		return true
	}
	_, err := idna.Lookup.ToASCII(domain)
	return err == nil
}

// ShownAddress returns addr as a person must see it: when its domain has
// characters outside ASCII, the ASCII form of the domain (punycode, the
// "xn--" spelling) follows in parentheses. A look-alike domain looks right, but
// its ASCII form does not. An address with a plain domain, and one whose domain
// has no ASCII form, stays as it is.
func ShownAddress(addr string) string {
	_, domain := splitAddress(addr)
	if isPrintableASCII(domain) {
		return addr
	}
	ascii, err := idna.Lookup.ToASCII(domain)
	if err != nil {
		return addr
	}
	return addr + " (" + ascii + ")"
}

// AddressWarning returns the text that a confirmation shows for an address with
// a character outside printable ASCII, or "" for any other address. It says
// that the address can look like another one, gives the ASCII form of the
// domain, and names each part of the domain that mixes scripts (writing
// systems) and the part before the @ when it is not ASCII.
func AddressWarning(addr string) string {
	if isPrintableASCII(addr) {
		return ""
	}
	local, domain := splitAddress(addr)
	var b strings.Builder
	fmt.Fprintf(&b, "%s has characters outside ASCII, so it can look like another address.", addr)
	if !isPrintableASCII(domain) {
		if ascii, err := idna.Lookup.ToASCII(domain); err == nil {
			fmt.Fprintf(&b, " Its domain in ASCII is %s.", ascii)
			domain = ascii
		}
		for _, label := range domainLabels(domain) {
			if scripts := mixedScripts(label); scripts != nil {
				fmt.Fprintf(&b, " The name %s mixes %s letters.", label, strings.Join(scripts, " and "))
			}
		}
	}
	if !isPrintableASCII(local) {
		b.WriteString(" The part before the @ has no ASCII form.")
	}
	return b.String()
}

// domainLabels returns the parts of a domain in their Unicode form, so each one
// can be read for the scripts that it uses.
func domainLabels(domain string) []string {
	if unicodeForm, err := idna.Lookup.ToUnicode(domain); err == nil {
		domain = unicodeForm
	}
	return strings.Split(domain, ".")
}

// mixedScripts returns the sorted names of the scripts of the letters of label
// when they do not belong together, and nil when they do. Digits, hyphens and
// marks have no script of their own. The test follows the "highly restrictive"
// level of Unicode TS 39: one script alone, or Latin with the scripts that
// Chinese, Japanese and Korean text use together.
func mixedScripts(label string) []string {
	seen := map[string]bool{}
	for _, r := range label {
		if name := scriptOf(r); name != "" {
			seen[name] = true
		}
	}
	var names []string
	eastAsian, latin, others := false, false, 0
	for name := range seen {
		names = append(names, name)
		switch name {
		case "Han", "Hiragana", "Katakana", "Hangul", "Bopomofo":
			eastAsian = true
		case "Latin":
			latin = true
		default:
			others++
		}
	}
	if others == 0 || (others == 1 && !latin && !eastAsian) {
		return nil
	}
	slices.Sort(names)
	return names
}

// scriptOf returns the name of the script of a letter, or "" for any other
// character. A character is in one script only, so the order of the map does
// not matter.
func scriptOf(r rune) string {
	if !unicode.IsLetter(r) {
		return ""
	}
	for name, table := range unicode.Scripts {
		if name != "Common" && name != "Inherited" && unicode.Is(table, r) {
			return name
		}
	}
	return ""
}

// IsPlainAddress reports whether s is an address that can stand outside the
// Untrusted wrapper of a tool result. That is an address in its plainest form:
// a dot-atom local part of at most 64 bytes (no space, no quote), and a domain
// of letters, digits and hyphens, in ASCII. Such a value has no room for a
// sentence. Anything else that a mail server reports as an address, also a real
// one with characters outside ASCII, is text that a third party wrote.
func IsPlainAddress(s string) bool {
	if len(s) > maxAddressBytes {
		return false
	}
	local, domain, found := strings.Cut(s, "@")
	return found && isDotAtom(local) && isLDHDomain(domain)
}

// isDotAtom reports whether s is a local part in the form dot-atom (RFC 5322).
func isDotAtom(s string) bool {
	if len(s) > maxLocalPartBytes {
		return false
	}
	for _, atom := range strings.Split(s, ".") {
		if atom == "" {
			return false
		}
		for i := range len(atom) {
			if c := atom[i]; !isLetterOrDigit(c) && strings.IndexByte(atomMarks, c) < 0 {
				return false
			}
		}
	}
	return true
}

// isLDHDomain reports whether every label of s has only letters, digits and
// hyphens, and no hyphen at either end.
func isLDHDomain(s string) bool {
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > maxLabelBytes || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := range len(label) {
			if c := label[i]; !isLetterOrDigit(c) && c != '-' {
				return false
			}
		}
	}
	return true
}

// isLetterOrDigit reports whether c is an ASCII letter or digit.
func isLetterOrDigit(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
