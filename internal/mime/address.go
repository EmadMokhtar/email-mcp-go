package mime

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

const (
	// MaxAddressBytes is the longest mail address. RFC 5321 limits a path to 256
	// bytes, with the two angle brackets.
	MaxAddressBytes = 254
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

// allowedScriptSets are the sets of scripts that may share one part of a domain
// name. They are the "highly restrictive" level of Unicode TS 39: Latin with the
// scripts that Japanese, Chinese and Korean text use. One script alone is
// always allowed.
var allowedScriptSets = [][]string{
	{"Han", "Hiragana", "Katakana", "Latin"},
	{"Bopomofo", "Han", "Latin"},
	{"Hangul", "Han", "Latin"},
}

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

// exactASCIIForm returns the ASCII form (punycode, the "xn--" spelling) of a
// domain outside ASCII. ok is false when the domain has no such form, and also
// when IDNA would change the domain before it encodes it: full-width letters,
// capital letters, another dot, or text that is not in composed form. The
// envelope holds the domain as it is, so the form of the changed text is not the
// form of that domain.
func exactASCIIForm(domain string) (ascii string, ok bool) {
	ascii, err := idna.Lookup.ToASCII(domain)
	if err != nil {
		return "", false
	}
	back, err := idna.Lookup.ToUnicode(ascii)
	if err != nil || back != domain {
		return "", false
	}
	return ascii, true
}

// HasExactASCIIDomain reports whether a person can check the domain of addr: it
// is plain ASCII, or it has an ASCII form that is the form of exactly this
// domain. What the user sees must be what the message goes to.
func HasExactASCIIDomain(addr string) bool {
	_, domain := splitAddress(addr)
	if isPrintableASCII(domain) {
		return true
	}
	_, ok := exactASCIIForm(domain)
	return ok
}

// ShownAddress returns addr as a person must see it: when its domain has
// characters outside ASCII, the ASCII form of the domain (punycode, the "xn--"
// spelling) follows in parentheses. A look-alike domain looks right, but its
// ASCII form does not. An address with a plain domain, and one for which
// HasExactASCIIDomain is false, stays as it is.
func ShownAddress(addr string) string {
	_, domain := splitAddress(addr)
	if isPrintableASCII(domain) {
		return addr
	}
	ascii, ok := exactASCIIForm(domain)
	if !ok {
		return addr
	}
	return addr + " (" + ascii + ")"
}

// AddressWarning returns the text that a confirmation shows for an address with
// a character outside printable ASCII, or "" for any other address. It says
// that the address can look like another one, and names each part of the domain
// that mixes scripts (writing systems) and the part before the @ when it is not
// ASCII. The ASCII form of the domain is not here, because ShownAddress puts it
// next to the address.
func AddressWarning(addr string) string {
	if isPrintableASCII(addr) {
		return ""
	}
	local, domain := splitAddress(addr)
	var b strings.Builder
	fmt.Fprintf(&b, "%s has characters outside ASCII, so it can look like another address.", addr)
	if !isPrintableASCII(domain) {
		for _, label := range strings.Split(domain, ".") {
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

// mixedScripts returns the sorted names of the scripts of the letters of label
// when no set of allowedScriptSets holds them all, and nil otherwise. Digits,
// hyphens and marks have no script of their own.
func mixedScripts(label string) []string {
	var names []string
	for _, r := range label {
		if name := scriptOf(r); name != "" && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	if len(names) < 2 {
		return nil
	}
	for _, allowed := range allowedScriptSets {
		if !slices.ContainsFunc(names, func(name string) bool { return !slices.Contains(allowed, name) }) {
			return nil
		}
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

// MarkInvisible replaces each character that a reader cannot see with U+FFFD,
// so a text that holds one does not read as the same text without it. Use it for
// a value in which each character counts, such as an address: removing a
// character gives another address. CleanUntrusted is for prose. It keeps the
// joiners, the variation selectors and some other invisible characters, because
// text in many languages needs them.
func MarkInvisible(s string) string {
	return strings.Map(func(r rune) rune {
		if isInvisibleRune(r) {
			return utf8.RuneError
		}
		return r
	}, s)
}

// isInvisibleRune reports whether r cannot be seen: what CleanUntrusted removes,
// and what it keeps although a reader cannot see it.
func isInvisibleRune(r rune) bool {
	switch {
	case isHiddenRune(r), isUnassigned(r), unicode.Is(unicode.Co, r): // Co is private use
		return true
	case r == 0x200C || r == 0x200D: // ZWNJ and ZWJ
		return true
	case r >= 0xFE00 && r <= 0xFE0F: // variation selectors
		return true
	case r == 0x2800: // braille blank
		return true
	}
	return false
}

// IsPlainAddress reports whether s is an address that can stand outside the
// Untrusted wrapper of a tool result. That is an address in its plainest form:
// a dot-atom local part of at most 64 bytes (no space, no quote), and a domain
// of letters, digits and hyphens, in ASCII. Such a value has no room for a
// sentence and no hidden character. Run it on the real value, not on a cleaned
// copy: a cleaned copy can be another address. Anything else that a mail server
// reports as an address, also a real one with characters outside ASCII, is text
// that a third party wrote.
func IsPlainAddress(s string) bool {
	if len(s) > MaxAddressBytes {
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
