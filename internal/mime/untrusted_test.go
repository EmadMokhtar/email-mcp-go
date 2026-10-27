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
	case r >= 0xE0000 && r <= 0xE007F,
		r >= 0x200B && r <= 0x200F,
		r >= 0x202A && r <= 0x202E,
		r >= 0x2066 && r <= 0x2069,
		r == 0xFEFF:
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
		{"zero width and direction marks", 0x200B, 0x200F},
		{"bidi embeddings and overrides", 0x202A, 0x202E},
		{"bidi isolates", 0x2066, 0x2069},
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
	for _, r := range []rune{0x200A, 0x2010, 0x202F, 0x206A, 0xE0080} {
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
