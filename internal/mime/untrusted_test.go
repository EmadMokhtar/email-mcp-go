package mime

import (
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// isRemovedRune reports whether CleanUntrusted must never let r through.
// It is written separately from the implementation on purpose. A mistake in
// one of them is then caught by the other.
func isRemovedRune(r rune) bool {
	switch {
	case r >= 0xE0000 && r <= 0xE007F, // tag characters
		r >= 0xE0100 && r <= 0xE01EF, // variation selectors supplement
		r == 0x200B,                  // zero width space
		r >= 0x200E && r <= 0x200F,   // direction marks
		r >= 0x202A && r <= 0x202E,   // bidi embeddings and overrides
		r >= 0x2060 && r <= 0x2064,   // word joiner and invisible operators
		r >= 0x2066 && r <= 0x2069,   // bidi isolates
		r >= 0x206A && r <= 0x206F,   // deprecated format characters
		r >= 0xFFF9 && r <= 0xFFFB,   // interlinear annotation
		r == 0x00AD,                  // soft hyphen
		r == 0x034F,                  // combining grapheme joiner
		r == 0x061C,                  // Arabic letter mark
		r == 0x115F, r == 0x1160,     // Hangul fillers
		r == 0x180E,              // Mongolian vowel separator
		r == 0x3164, r == 0xFFA0, // Hangul fillers
		r == 0xFEFF,                // byte order mark
		r >= 0x180B && r <= 0x180F, // Mongolian free variation selectors and vowel separator
		r == 0x17B4, r == 0x17B5,   // Khmer inherent vowels
		r == 0xFFFC,                // object replacement character
		r >= 0xFFF0 && r <= 0xFFF8, // unassigned, next to the interlinear annotation
		r == 0x2065:                // unassigned, among the invisible operators
		return true
	case r == 0x200C, r == 0x200D: // ZWNJ and ZWJ are kept
		return false
	case unicode.Is(unicode.Cf, r): // every other format character
		return true
	}
	isControl := r < 0x20 || (r >= 0x7F && r <= 0x9F)
	return isControl && r != '\n' && r != '\t'
}

func TestCleanUntrustedRemovesInvisible(t *testing.T) {
	require.Equal(t, "paymeok", CleanUntrusted("pay\U0000200Bme\U000E0041\U0000202Eok"))
}

func TestCleanUntrustedRemovesWholeRanges(t *testing.T) {
	ranges := []struct {
		name     string
		from, to rune
	}{
		{"tag characters", 0xE0000, 0xE007F},
		{"variation selectors supplement", 0xE0100, 0xE01EF},
		{"zero width space", 0x200B, 0x200B},
		{"direction marks", 0x200E, 0x200F},
		{"bidi embeddings and overrides", 0x202A, 0x202E},
		{"word joiner and invisible operators", 0x2060, 0x2064},
		{"bidi isolates", 0x2066, 0x2069},
		{"deprecated format characters", 0x206A, 0x206F},
		{"interlinear annotation", 0xFFF9, 0xFFFB},
		{"soft hyphen", 0x00AD, 0x00AD},
		{"combining grapheme joiner", 0x034F, 0x034F},
		{"Arabic letter mark", 0x061C, 0x061C},
		{"Hangul choseong filler", 0x115F, 0x115F},
		{"Hangul jungseong filler", 0x1160, 0x1160},
		{"Hangul filler", 0x3164, 0x3164},
		{"halfwidth Hangul filler", 0xFFA0, 0xFFA0},
		{"Mongolian vowel separator", 0x180E, 0x180E},
		{"byte order mark", 0xFEFF, 0xFEFF},
	}
	for _, rg := range ranges {
		t.Run(rg.name, func(t *testing.T) {
			for r := rg.from; r <= rg.to; r++ {
				require.Equal(t, "ab", CleanUntrusted("a"+string(r)+"b"), "U+%04X", r)
			}
		})
	}
}

func TestCleanUntrustedKeepsRunesOutsideTheRanges(t *testing.T) {
	// Each rune sits right next to a removed range.
	for _, r := range []rune{
		0x00AC, 0x00AE, // around the soft hyphen
		0x180A, 0x1810, // around the Mongolian variation selectors and vowel separator
		0x17B3, 0x17B6, // around the Khmer inherent vowels
		0x200A, 0x2010, // around the zero width space and the direction marks
		0x202F,         // after the bidi overrides
		0x205F,         // before the word joiner
		0x2070,         // after the deprecated format characters
		0x061B, 0x061D, // around the Arabic letter mark
		0x034E, 0x0350, // around the combining grapheme joiner
		0x115E, 0x1161, // around the Hangul fillers (1161 is a normal vowel)
		0x3163, 0x3165, // around the Hangul filler
		0xFF9F, 0xFFA1, // around the halfwidth Hangul filler
		0xFFEE, 0xFFFD, // around the unassigned code points, the interlinear annotation and U+FFFC
		0xFF01,  // after the byte order mark
		0xE0080, // after the tag characters
		0xE01F0, // after the variation selectors supplement
	} {
		in := "a" + string(r) + "b"
		require.Equal(t, in, CleanUntrusted(in), "U+%04X", r)
	}
}

// Persian, Urdu and other Arabic-script text needs ZWNJ (U+200C). Emoji
// sequences need ZWJ (U+200D) and the variation selectors U+FE00 to U+FE0F.
func TestCleanUntrustedKeepsJoinersAndEmojiSelectors(t *testing.T) {
	for _, in := range []string{
		"a\U0000200Cb", // ZWNJ
		"a\U0000200Db", // ZWJ
		// Persian "mi-ravam" is written with a ZWNJ after "mi".
		"\U00000645\U000006CC\U0000200C\U00000631\U00000648\U00000645",
		// Family emoji: man, ZWJ, woman, ZWJ, girl.
		"\U0001F468\U0000200D\U0001F469\U0000200D\U0001F467",
		// Heart with emoji presentation selector.
		"\U00002764\U0000FE0F",
	} {
		require.Equal(t, in, CleanUntrusted(in), "%q", in)
	}
	for r := rune(0xFE00); r <= 0xFE0F; r++ {
		in := "a" + string(r) + "b"
		require.Equal(t, in, CleanUntrusted(in), "U+%04X", r)
	}
}

func TestCleanUntrustedControlCharacters(t *testing.T) {
	// Every control character goes, except tab and newline. A lone CR turns
	// into a newline (see the line-ending test).
	for r := rune(0); r <= 0x9F; r++ {
		if r >= 0x20 && r < 0x7F {
			continue // printable ASCII is checked below
		}
		want := ""
		switch r {
		case '\n', '\t':
			want = string(r)
		case '\r':
			want = "\n"
		}
		require.Equal(t, want, CleanUntrusted(string(r)), "U+%04X", r)
	}
	require.Equal(t, "[31mred", CleanUntrusted("\x1b[31mred"), "escape byte goes, the rest stays")
}

func TestCleanUntrustedKeepsPrintableASCII(t *testing.T) {
	for r := rune(0x20); r < 0x7F; r++ {
		require.Equal(t, string(r), CleanUntrusted(string(r)))
	}
}

func TestCleanUntrustedLineEndings(t *testing.T) {
	require.Equal(t, "a\nb\nc\n", CleanUntrusted("a\r\nb\rc\n"))
	require.Equal(t, "\n\n", CleanUntrusted("\r\r\n"), "a lone CR and a CRLF are two line breaks")
	require.Equal(t, "a\tb\nc", CleanUntrusted("a\tb\nc"), "tab and newline stay")
}

func TestCleanUntrustedInvalidUTF8(t *testing.T) {
	require.Equal(t, "a\U0000FFFDb\U0000FFFD", CleanUntrusted("a\xffb\xc3"))
	for _, in := range []string{"\xff\xfe\xfd", "ok\xe2\x82", "\xf0\x9f\x98", "\xed\xa0\x80"} {
		require.True(t, utf8.ValidString(CleanUntrusted(in)), "%q", in)
	}
}

func TestCleanUntrustedKeepsOrdinaryText(t *testing.T) {
	in := "Hello, h\U000000E9llo \U00004E16\U0000754C \U0001F600\ttab\nnext line"
	require.Equal(t, in, CleanUntrusted(in))
	require.Equal(t, "", CleanUntrusted(""))
}

func TestCleanUntrustedIsIdempotent(t *testing.T) {
	in := "a\r\n\U0000200B\xff\x00b\U000E0041"
	once := CleanUntrusted(in)
	require.Equal(t, once, CleanUntrusted(once))
	require.Equal(t, "a\n"+"\U0000FFFD"+"b", once)
}

// The review of PR #3 (item 3) showed that format characters outside the list
// passed. Now every character of the Unicode category Cf goes, except ZWNJ and
// ZWJ, and so do a few characters that are not Cf but are invisible.
func TestCleanUntrustedRemovesEveryFormatCharacter(t *testing.T) {
	named := []struct {
		name string
		r    rune
	}{
		{"musical symbol begin beam (Cf)", 0x1D173},
		{"musical symbol end phrase (Cf)", 0x1D17A},
		{"shorthand format letter overlap (Cf)", 0x1BCA0},
		{"shorthand format up step (Cf)", 0x1BCA3},
		{"Egyptian hieroglyph vertical joiner (Cf)", 0x13430},
		{"Arabic number sign (Cf)", 0x0600},
		{"Arabic end of ayah (Cf)", 0x06DD},
		{"Syriac abbreviation mark (Cf)", 0x070F},
		{"language tag (Cf)", 0xE0001},
		{"Mongolian free variation selector one", 0x180B},
		{"Mongolian free variation selector three", 0x180D},
		{"Mongolian free variation selector four", 0x180F},
		{"Khmer vowel inherent aq", 0x17B4},
		{"Khmer vowel inherent aa", 0x17B5},
		{"object replacement character", 0xFFFC},
		{"unassigned U+2065", 0x2065},
		{"unassigned U+FFF0", 0xFFF0},
		{"unassigned U+FFF8", 0xFFF8},
	}
	for _, tc := range named {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, "ab", CleanUntrusted("a"+string(tc.r)+"b"), "U+%04X", tc.r)
		})
	}
	// Every Cf code point that Go knows.
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if !unicode.Is(unicode.Cf, r) || r == 0x200C || r == 0x200D {
			continue
		}
		require.Equal(t, "ab", CleanUntrusted("a"+string(r)+"b"), "U+%04X", r)
	}
	// The unassigned code points in the two format ranges, and nothing else
	// there that is assigned and visible.
	for _, rg := range [][2]rune{{0x2060, 0x206F}, {0xFFF0, 0xFFF8}} {
		for r := rg[0]; r <= rg[1]; r++ {
			require.Equal(t, "ab", CleanUntrusted("a"+string(r)+"b"), "U+%04X", r)
		}
	}
}

// Since Go 1.25, unicode.C holds the unassigned code points (Cn) too, so
// isUnassigned uses the Cn table and not "in no other category".
func TestIsUnassigned(t *testing.T) {
	require.True(t, unicode.Is(unicode.C, 0x2065), "unicode.C holds Cn")
	require.True(t, isUnassigned(0x2065))
	require.True(t, isUnassigned(0xFFF0))
	require.False(t, isUnassigned('a'))
	require.False(t, isUnassigned(0x200B), "Cf is assigned")
	require.False(t, isUnassigned(0xE000), "private use is assigned")
	require.False(t, isUnassigned(0x0000), "Cc is assigned")
}

// ZWNJ, ZWJ and VS16 stay, also in the text that needs them.
func TestCleanUntrustedKeepsTheJoinersAfterTheFormatRule(t *testing.T) {
	for _, in := range []string{
		"\u200c", "\u200d", "\ufe0f",
		// Persian "nemi-khaham" (I do not want) with a ZWNJ.
		"\u0646\u0645\u06cc\u200c\u062e\u0648\u0627\u0647\u0645",
		// Rainbow flag: white flag, VS16, ZWJ, rainbow.
		"\U0001F3F3\uFE0F\u200D\U0001F308",
		// Woman technologist: woman, ZWJ, laptop.
		"\U0001F469\u200D\U0001F4BB",
	} {
		require.Equal(t, in, CleanUntrusted(in), "%q", in)
	}
}
