package mime

import (
	"testing"
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
		r == 0xFEFF: // byte order mark
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
		0x180D, 0x180F, // around the Mongolian vowel separator
		0x200A, 0x2010, // around the zero width space and the direction marks
		0x202F,         // after the bidi overrides
		0x205F,         // before the word joiner
		0x2070,         // after the deprecated format characters
		0x061B, 0x061D, // around the Arabic letter mark
		0x034E, 0x0350, // around the combining grapheme joiner
		0x115E, 0x1161, // around the Hangul fillers (1161 is a normal vowel)
		0x3163, 0x3165, // around the Hangul filler
		0xFF9F, 0xFFA1, // around the halfwidth Hangul filler
		0xFFF8, 0xFFFC, // around the interlinear annotation
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
