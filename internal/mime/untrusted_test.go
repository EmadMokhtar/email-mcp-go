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
		r >= 0xE0080 && r <= 0xE00FF,   // default ignorable, not assigned
		r >= 0xE0100 && r <= 0xE01EF,   // variation selectors supplement
		r >= 0xE01F0 && r <= 0xE0FFF,   // default ignorable, not assigned
		r >= 0xE000 && r <= 0xF8FF,     // private use
		r >= 0xF0000 && r <= 0xFFFFD,   // private use, plane 15
		r >= 0x100000 && r <= 0x10FFFD, // private use, plane 16
		r >= 0xFE00 && r <= 0xFE0E,     // variation selectors except the emoji one
		r == 0x2800,                    // braille blank
		r == 0x200B,                    // zero width space
		r >= 0x200E && r <= 0x200F,     // direction marks
		r >= 0x202A && r <= 0x202E,     // bidi embeddings and overrides
		r >= 0x2060 && r <= 0x2064,     // word joiner and invisible operators
		r >= 0x2066 && r <= 0x2069,     // bidi isolates
		r >= 0x206A && r <= 0x206F,     // deprecated format characters
		r >= 0xFFF9 && r <= 0xFFFB,     // interlinear annotation
		r == 0x00AD,                    // soft hyphen
		r == 0x034F,                    // combining grapheme joiner
		r == 0x061C,                    // Arabic letter mark
		r == 0x115F, r == 0x1160,       // Hangul fillers
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
		{"default ignorable after the tag characters", 0xE0080, 0xE00FF},
		{"variation selectors supplement", 0xE0100, 0xE01EF},
		{"default ignorable after the selectors", 0xE01F0, 0xE0FFF},
		{"private use", 0xE000, 0xF8FF},
		{"variation selectors except the emoji one", 0xFE00, 0xFE0E},
		{"braille blank", 0x2800, 0x2800},
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
		0xFF01,         // after the byte order mark
		0xE1000,        // after the default ignorable code points of plane 14
		0x27FF, 0x2801, // around the braille blank
		0xD7FF, 0xF900, // around the private use area
		0xFDFF, 0xFE10, // around the variation selectors
	} {
		in := "a" + string(r) + "b"
		require.Equal(t, in, CleanUntrusted(in), "U+%04X", r)
	}
}

// Text in the scripts that join letters needs ZWNJ (U+200C) and ZWJ (U+200D),
// and emoji sequences need ZWJ and VS16 (U+FE0F).
func TestCleanUntrustedKeepsTheJoinersThatChangeWhatIsSeen(t *testing.T) {
	for name, in := range map[string]string{
		"Persian with ZWNJ":              "\U00000645\U000006CC\U0000200C\U00000631\U00000648\U00000645",
		"Persian with ZWNJ, second":      "\u0646\u0645\u06cc\u200c\u062e\u0648\u0627\u0647\u0645",
		"Arabic letter, ZWNJ, a space":   "\u0647\u200c \u0647",
		"Arabic letter, ZWJ at the end":  "\u0647\u200d",
		"ZWJ before an Arabic letter":    "\u200d\u0647",
		"Arabic with a mark":             "\u0628\u064e\u200c\u0628",
		"Devanagari conjunct":            "\u0915\u094d\u200d\u0937",
		"Devanagari, no conjunct":        "\u0915\u094d\u200c\u0937",
		"Bengali":                        "\u0995\u09cd\u200c\u09b7",
		"Malayalam at the end of a word": "\u0d28\u0d4d\u200d \u0d28",
		"Sinhala":                        "\u0d9a\u0dca\u200d\u0dba",
		"family":                         "\U0001F468\U0000200D\U0001F469\U0000200D\U0001F467",
		"woman technologist":             "\U0001F469\u200D\U0001F4BB",
		"with a skin tone":               "\U0001F469\U0001F3FD\u200D\U0001F4BB",
		"rainbow flag":                   "\U0001F3F3\uFE0F\u200D\U0001F308",
		"heart on fire":                  "\u2764\uFE0F\u200D\U0001F525",
		"man running":                    "\U0001F3C3\u200D\u2642\uFE0F",
		"heart":                          "\U00002764\U0000FE0F",
		"copyright":                      "\u00A9\uFE0F",
		"keycap digit":                   "1\uFE0F\u20E3",
		"keycap hash":                    "#\uFE0F\u20E3",
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, in, CleanUntrusted(in))
			require.Zero(t, HiddenRunes(in))
		})
	}
}

// A joiner that changes nothing a reader can see is a hidden character. Between
// Latin letters, for example, it could carry data that nobody sees.
func TestCleanUntrustedRemovesTheJoinersThatChangeNothing(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"ZWNJ between Latin letters": {"a\u200Cb", "ab"},
		"ZWJ between Latin letters":  {"a\u200Db", "ab"},
		"VS16 after a Latin letter":  {"a\uFE0Fb", "ab"},
		"Cyrillic":                   {"\u0430\u200C\u0431", "\u0430\u0431"},
		"alone":                      {"\u200C\u200D\uFE0F", ""},
		"a digit that is no keycap":  {"1\uFE0F", "1"},
		"a keycap mark after a word": {"a\uFE0F\u20E3", "a\u20E3"},
		"ZWJ after an emoji":         {"\U0001F600\u200D", "\U0001F600"},
		"ZWJ before an emoji":        {"\u200D\U0001F600", "\U0001F600"},
		"ZWJ between emoji and text": {"\U0001F600\u200Db", "\U0001F600b"},
		"a run of VS16":              {"\u2764\uFE0F\uFE0F\uFE0F", "\u2764\uFE0F"},
		"a run of ZWJ between emoji": {"\U0001F600\u200D\u200D\U0001F600", "\U0001F600\u200D\U0001F600"},
		"a run of joiners":           {"\u0628\u200C\u200C\u200D\u200C\u0628", "\u0628\u200C\u0628"},
		"after a hidden character":   {"\u0628\u200B\u200C\u0628", "\u0628\u200C\u0628"},
	} {
		t.Run(name, func(t *testing.T) {
			got := CleanUntrusted(tc.in)
			require.Equal(t, tc.want, got)
			require.Equal(t, got, CleanUntrusted(got), "cleaning twice must change nothing")
		})
	}
}

func TestHiddenRunes(t *testing.T) {
	require.Zero(t, HiddenRunes(""))
	require.Zero(t, HiddenRunes("plain text\r\nwith\rlines\n\tand a tab"), "a line break is not hidden")
	require.Equal(t, 1, HiddenRunes("a\u200Bb"))
	require.Equal(t, 3, HiddenRunes("\x07a\U000E0041b\u2800"))
	require.Equal(t, 2, HiddenRunes("a\u200Cb\uFE0F"))
	require.Zero(t, HiddenRunes("\xff"), "invalid UTF-8 becomes U+FFFD, which a reader sees")
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
