// Package mime holds helpers for mail content written by other people.
//
// Mail text is handed to an AI agent, so it is a way to send hidden
// instructions (prompt injection). The functions here remove characters a
// reader cannot see, and turn HTML into plain text without the parts that a
// reader cannot see either.
//
// The package also writes mail: Build makes a new message, a reply or a
// forward, and FromDraft prepares a saved draft for sending. Both keep Bcc
// recipients out of the headers that are sent, and neither lets a value add a
// header.
package mime

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// CleanUntrusted makes text from a third party safe to show to an agent.
//
// It does these things, in this order:
//   - Replaces each run of bytes that is not valid UTF-8 with U+FFFD, so the
//     result is always valid UTF-8.
//   - Turns "\r\n" and a lone "\r" into "\n".
//   - Removes every character of the Unicode category Cf (format characters),
//     except the zero width non-joiner (U+200C) and the zero width joiner
//     (U+200D). Cf holds, among others, the zero width space, the direction
//     marks, the bidi embeddings, overrides and isolates, the word joiner and
//     invisible operators, the soft hyphen, the Arabic letter mark, the
//     Mongolian vowel separator, the interlinear annotation marks, the byte
//     order mark, the tag characters and the musical, shorthand and
//     hieroglyph format controls. The table is the one of the Go version that
//     builds the program.
//   - Removes these characters, which are not Cf but cannot be seen or are
//     used to hide text: the variation selectors supplement (U+E0100 to
//     U+E01EF, used to hide data), the whole tag block (U+E0000 to U+E007F,
//     also the code points in it that are not assigned), the combining
//     grapheme joiner (U+034F), the Hangul fillers (U+115F, U+1160, U+3164,
//     U+FFA0), the Mongolian free variation selectors (U+180B to U+180F), the
//     Khmer inherent vowels (U+17B4 and U+17B5), the object replacement
//     character (U+FFFC), and the code points that are not assigned in the
//     format ranges U+2060 to U+206F and U+FFF0 to U+FFF8.
//   - Removes control characters (Cc), except "\n" and "\t".
//
// It keeps the zero width non-joiner (U+200C) and the zero width joiner
// (U+200D), because Persian, Urdu and other Arabic-script text, and emoji
// sequences, need them. It also keeps the variation selectors U+FE00 to
// U+FE0F, which choose how an emoji is drawn. Every other character stays.
func CleanUntrusted(s string) string {
	s = strings.ToValidUTF8(s, string(utf8.RuneError))
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if !isHiddenRune(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isHiddenRune reports whether CleanUntrusted removes r.
func isHiddenRune(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case r == 0x200C || r == 0x200D: // ZWNJ and ZWJ are kept
		return false
	case isFormatRune(r) || unicode.IsControl(r):
		return true
	case r >= 0xE0000 && r <= 0xE007F: // tag characters, also the unassigned ones
		return true
	case r >= 0xE0100 && r <= 0xE01EF: // variation selectors supplement
		return true
	case r == 0x034F: // combining grapheme joiner
		return true
	case r == 0x115F || r == 0x1160 || r == 0x3164 || r == 0xFFA0: // Hangul fillers
		return true
	case r >= 0x180B && r <= 0x180F: // Mongolian free variation selectors (U+180E is Cf)
		return true
	case r == 0x17B4 || r == 0x17B5: // Khmer inherent vowels
		return true
	case r == 0xFFFC: // object replacement character
		return true
	case (r >= 0x2060 && r <= 0x206F) || (r >= 0xFFF0 && r <= 0xFFF8):
		// Format ranges: a code point that is not assigned yet may become a
		// format character in a later version of Unicode.
		return isUnassigned(r)
	}
	return false
}

// isFormatRune reports whether r is in the Unicode category Cf (format
// characters), such as a zero width space or a bidi override. It is the one
// place where the category test of CleanUntrusted is written.
func isFormatRune(r rune) bool {
	return unicode.Is(unicode.Cf, r)
}

// isUnassigned reports whether r is not assigned in the Unicode tables of Go
// (the category Cn). Since Go 1.25 the unicode package has a table for Cn, and
// unicode.C holds Cn too, so the test "in no other category" would never be
// true.
func isUnassigned(r rune) bool {
	return unicode.Is(unicode.Cn, r)
}
