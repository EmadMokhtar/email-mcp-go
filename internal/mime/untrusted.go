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
//     (U+200D), which the last rule handles, and the few that a reader sees
//     (see isVisibleFormatRune). Cf holds, among others, the zero width space,
//     the direction marks, the bidi embeddings, overrides and isolates, the
//     word joiner and invisible operators, the soft hyphen, the Arabic letter
//     mark, the Mongolian vowel separator, the interlinear annotation marks, the
//     byte order mark, the tag characters and the musical, shorthand and
//     hieroglyph format controls. The table is the one of the Go version that
//     builds the program.
//   - Removes these characters, which are not Cf but cannot be seen or are
//     used to hide text: all of U+E0000 to U+E0FFF (the tag block, the
//     variation selectors supplement U+E0100 to U+E01EF, which is used to hide
//     data, and the code points around them, also those that are not
//     assigned), the variation selectors U+FE00 to U+FE0E, the private use
//     areas (no mail font draws them), the braille blank (U+2800), the
//     combining grapheme joiner (U+034F), the Hangul fillers (U+115F, U+1160,
//     U+3164, U+FFA0), the Mongolian free variation selectors (U+180B to
//     U+180F), the Khmer inherent vowels (U+17B4 and U+17B5), the object
//     replacement character (U+FFFC), and the code points that are not
//     assigned in the format ranges U+2060 to U+206F and U+FFF0 to U+FFF8.
//   - Removes control characters (Cc), except "\n" and "\t".
//   - Removes U+200C, U+200D and U+FE0F (variation selector 16) unless they
//     change what a reader sees (see keepsJoiner). They stay next to a letter
//     of a script that joins letters, such as Arabic or Devanagari, and inside
//     an emoji sequence. Elsewhere, for example between Latin letters, they
//     change nothing on the screen, and text can hide data in them. A run of
//     them is cut to what an emoji sequence or a word needs, so a run cannot
//     carry data either.
//
// Every other character stays.
func CleanUntrusted(s string) string {
	return removeHidden(normalizeText(s))
}

// HiddenRunes returns how many characters CleanUntrusted removes from s. The
// line breaks that it rewrites do not count, and neither do bytes that are not
// valid UTF-8: they become U+FFFD, which a reader sees.
func HiddenRunes(s string) int {
	_, hidden := CleanAndCount(s)
	return hidden
}

// CleanAndCount returns CleanUntrusted(s) and HiddenRunes(s), with one pass over
// the text.
func CleanAndCount(s string) (clean string, hidden int) {
	s = normalizeText(s)
	clean = removeHidden(s)
	return clean, utf8.RuneCountInString(s) - utf8.RuneCountInString(clean)
}

// normalizeText returns s as valid UTF-8 with each line break written as "\n".
func normalizeText(s string) string {
	s = strings.ToValidUTF8(s, string(utf8.RuneError))
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// removeHidden removes the hidden characters of s, which must be valid UTF-8.
//
// A joiner is tested against the last character that stayed and the next one
// that is not always removed. So cleaning the result again changes nothing.
func removeHidden(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	var prev rune // the last character that stayed; 0 is never one
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if isHiddenRune(r) || (isJoiner(r) && !keepsJoiner(prev, r, nextShown(s[i:]))) {
			continue
		}
		b.WriteRune(r)
		prev = r
	}
	return b.String()
}

// nextShown returns the first character of s that is not always removed, or 0.
// It stops at a joiner, so each hidden character is read at most twice.
func nextShown(s string) rune {
	for _, r := range s {
		if !isHiddenRune(r) {
			return r
		}
	}
	return 0
}

// isHiddenRune reports whether CleanUntrusted removes r wherever it is. For a
// joiner the answer depends on the text around it (see keepsJoiner).
func isHiddenRune(r rune) bool {
	switch {
	case r == '\n' || r == '\t' || isJoiner(r) || isVisibleFormatRune(r):
		return false
	case isFormatRune(r) || unicode.IsControl(r):
		return true
	case r >= 0xE0000 && r <= 0xE0FFF: // tag characters and selectors, also the unassigned ones
		return true
	case r >= 0xFE00 && r <= 0xFE0E: // variation selectors, except the one for emoji
		return true
	case unicode.Is(unicode.Co, r): // private use
		return true
	case r == 0x2800: // braille blank
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

const (
	zwnj   = 0x200C // zero width non-joiner
	zwj    = 0x200D // zero width joiner
	vs16   = 0xFE0F // variation selector 16: draw the character as an emoji
	keycap = 0x20E3 // combining enclosing keycap
)

// isJoiner reports whether r is one of the characters that stay only in some
// places (see keepsJoiner).
func isJoiner(r rune) bool {
	return r == zwnj || r == zwj || r == vs16
}

// keepsJoiner reports whether the joiner r changes what a reader sees. prev is
// the last character that stayed, and next is the next one that is not always
// removed.
//
// VS16 stays after a character that can be an emoji, and in a keycap such as
// "1", VS16, U+20E3. ZWJ stays between two emoji. ZWNJ and ZWJ stay next to a
// letter of a script that joins letters: there they change the shape of the
// letters. Either side is enough, because such text also has them at the end
// of a word, as in Malayalam or in a Persian abbreviation. Only one stays in a
// row. The one case of two is VS16 and then ZWJ in an emoji sequence.
func keepsJoiner(prev, r, next rune) bool {
	if r == vs16 {
		return isEmojiBase(prev) || (isKeycapBase(prev) && next == keycap)
	}
	if r == zwj && (isEmojiBase(prev) || prev == vs16) && isEmojiBase(next) {
		return true
	}
	if isJoiner(prev) {
		return false
	}
	return joinsLetters(prev) || joinsLetters(next)
}

// joiningScripts are the scripts in which ZWNJ and ZWJ change how letters look:
// the Arabic family, where letters join, and the Brahmic scripts, where they
// choose the form of a conjunct.
var joiningScripts = []*unicode.RangeTable{
	unicode.Arabic, unicode.Syriac, unicode.Mandaic, unicode.Nko, unicode.Mongolian,
	unicode.Adlam, unicode.Hanifi_Rohingya,
	unicode.Devanagari, unicode.Bengali, unicode.Gurmukhi, unicode.Gujarati, unicode.Oriya,
	unicode.Tamil, unicode.Telugu, unicode.Kannada, unicode.Malayalam, unicode.Sinhala,
	unicode.Khmer, unicode.Myanmar, unicode.Tibetan, unicode.Balinese, unicode.Javanese,
}

// joinsLetters reports whether r is in one of joiningScripts. The first test
// spares Latin text the search: the first of the scripts starts at U+0600.
func joinsLetters(r rune) bool {
	return r >= 0x0600 && unicode.In(r, joiningScripts...)
}

// isEmojiBase reports whether r can be drawn as an emoji. The unicode package
// has no table of emoji, so this is the blocks that hold them, and the few that
// are outside these blocks. It is wider than the list of Unicode. That does no
// harm: it only says where one joiner may stay.
func isEmojiBase(r rune) bool {
	switch {
	case r >= 0x1F000 && r <= 0x1FAFF, // pictographs, flags and skin tones
		r >= 0x2190 && r <= 0x21FF, // arrows
		r >= 0x2300 && r <= 0x23FF, // technical symbols, such as the watch
		r >= 0x25A0 && r <= 0x27BF, // shapes, symbols and dingbats
		r >= 0x2900 && r <= 0x297F, // more arrows
		r >= 0x2B00 && r <= 0x2BFF: // more symbols, such as the star
		return true
	}
	switch r {
	case 0x00A9, 0x00AE, 0x203C, 0x2049, 0x2122, 0x2139, 0x24C2, 0x3030, 0x303D, 0x3297, 0x3299:
		return true
	}
	return false
}

// isKeycapBase reports whether r can start a keycap sequence.
func isKeycapBase(r rune) bool {
	return (r >= '0' && r <= '9') || r == '#' || r == '*'
}

// isVisibleFormatRune reports whether r is a format character (Cf) that a reader
// sees: it draws a sign around the digits that follow it. These are the Arabic
// number signs (U+0600 to U+0605), the Arabic end of ayah (U+06DD), the Syriac
// abbreviation mark (U+070F), the Arabic pound and piastre marks above (U+0890,
// U+0891), the Arabic disputed end of ayah (U+08E2), and the Kaithi number signs
// (U+110BD, U+110CD). The direction marks are not among them: LRM, RLM and ALM
// draw nothing.
func isVisibleFormatRune(r rune) bool {
	switch {
	case r >= 0x0600 && r <= 0x0605:
		return true
	case r == 0x06DD, r == 0x070F, r == 0x0890, r == 0x0891, r == 0x08E2, r == 0x110BD, r == 0x110CD:
		return true
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
