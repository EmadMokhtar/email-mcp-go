// Package mime holds helpers for mail content written by other people.
//
// Mail text is handed to an AI agent, so it is a way to send hidden
// instructions (prompt injection). The functions here remove characters a
// reader cannot see, and turn HTML into plain text without the parts that a
// reader cannot see either.
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
//   - Removes characters that a reader cannot see, or that change how text is
//     displayed:
//     the Unicode tag characters (U+E0000 to U+E007F), the variation selectors
//     supplement (U+E0100 to U+E01EF, used to hide data), the zero width space
//     (U+200B), the direction marks (U+200E and U+200F), the bidi embeddings
//     and overrides (U+202A to U+202E), the word joiner and invisible
//     operators (U+2060 to U+2064), the bidi isolates (U+2066 to U+2069), the
//     soft hyphen (U+00AD), the Mongolian vowel separator (U+180E) and the
//     byte order mark (U+FEFF).
//   - Removes control characters, except "\n" and "\t".
//
// It keeps the zero width non-joiner (U+200C) and the zero width joiner
// (U+200D), because Persian, Urdu and other Arabic-script text, and emoji
// sequences, need them. It also keeps the variation selectors U+FE00 to
// U+FE0F, which choose how an emoji is drawn.
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
	case r >= 0xE0000 && r <= 0xE007F: // tag characters
		return true
	case r >= 0xE0100 && r <= 0xE01EF: // variation selectors supplement
		return true
	case r == 0x200B: // zero width space (U+200C and U+200D are kept)
		return true
	case r == 0x200E || r == 0x200F: // direction marks
		return true
	case r >= 0x202A && r <= 0x202E: // bidi embeddings and overrides
		return true
	case r >= 0x2060 && r <= 0x2064: // word joiner and invisible operators
		return true
	case r >= 0x2066 && r <= 0x2069: // bidi isolates
		return true
	case r == 0x00AD: // soft hyphen
		return true
	case r == 0x180E: // Mongolian vowel separator
		return true
	case r == 0xFEFF: // byte order mark, also a zero width no-break space
		return true
	}
	return unicode.IsControl(r) && r != '\n' && r != '\t'
}
