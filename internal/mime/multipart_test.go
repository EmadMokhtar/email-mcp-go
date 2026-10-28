package mime

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/require"
)

// splitParts runs the multipart reader over raw and returns the body of each
// part that is not skipped.
func splitParts(t *testing.T, raw, boundary string) []string {
	t.Helper()
	var bodies []string
	mr := newMultipartReader(strings.NewReader(raw), boundary)
	for {
		_, body, tooBig, ok := mr.next()
		if !ok {
			return bodies
		}
		if tooBig {
			continue
		}
		b, err := io.ReadAll(body)
		require.NoError(t, err)
		bodies = append(bodies, string(b))
	}
}

func TestMultipartReaderSplitsParts(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "crlf",
			raw:  "--b\r\n\r\none\r\n--b\r\n\r\ntwo\r\n--b--\r\n",
			want: []string{"one", "two"},
		},
		{
			name: "lf only",
			raw:  "--b\n\none\n--b\n\ntwo\n--b--\n",
			want: []string{"one", "two"},
		},
		{
			name: "preamble and epilogue are not parts",
			raw:  "preamble\r\nmore\r\n--b\r\n\r\none\r\n--b--\r\nepilogue\r\n--b\r\n\r\nnot a part\r\n",
			want: []string{"one"},
		},
		{
			name: "empty part",
			raw:  "--b\r\nX: y\r\n\r\n\r\n--b\r\n\r\nnext\r\n--b--",
			want: []string{"", "next"},
		},
		{
			name: "part without a body and without an empty line",
			raw:  "--b\r\nX: y\r\n\r\n--b\r\n\r\nnext\r\n--b--",
			want: []string{"", "next"},
		},
		{
			name: "blank lines and bare carriage returns stay in the body",
			raw:  "--b\r\n\r\na\r\n\r\nb\rc\r\n\r\n--b--",
			want: []string{"a\r\n\r\nb\rc\r\n"},
		},
		{
			name: "spaces and tabs after a boundary",
			raw:  "--b \t\r\n\r\none\r\n--b  \r\n\r\ntwo\r\n--b--\t \r\n",
			want: []string{"one", "two"},
		},
		{
			name: "lines that only start like the boundary",
			raw:  "--b\r\n\r\n--bx\r\n--b-\r\n--b--x\r\n x--b\r\n--b\r\n\r\ntwo\r\n--b--",
			want: []string{"--bx\r\n--b-\r\n--b--x\r\n x--b", "two"},
		},
		{
			name: "closing boundary without a line ending",
			raw:  "--b\r\n\r\none\r\n--b--",
			want: []string{"one"},
		},
		{
			name: "no closing boundary",
			raw:  "--b\r\n\r\none\r\n--b\r\n\r\ntwo\r\n",
			want: []string{"one", "two\r\n"},
		},
		{
			name: "opening boundary at the very end is not a part",
			raw:  "--b\r\n\r\none\r\n--b",
			want: []string{"one"},
		},
		{
			name: "no boundary at all",
			raw:  "just text\r\nmore text\r\n",
			want: nil,
		},
		{
			name: "empty input",
			raw:  "",
			want: nil,
		},
		{
			name: "header without an end",
			raw:  "--b\r\nX: y",
			want: []string{""},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, splitParts(t, tc.raw, "b"))
		})
	}
}

func TestMultipartReaderLinesLongerThanTheBuffer(t *testing.T) {
	// The sizes put the end of a line, and its "\r\n", on both sides of the
	// end of the buffer.
	sizes := []int{multipartBufSize - 2, multipartBufSize - 1, multipartBufSize, multipartBufSize + 1, 3*multipartBufSize + 7}
	for _, n := range sizes {
		line := strings.Repeat("a", n)
		for name, ending := range map[string]string{"crlf": "\r\n", "lf": "\n"} {
			raw := "--b" + ending + ending + line + ending + "tail" + ending + "--b" + ending + ending + line + ending + "--b--" + ending
			got := splitParts(t, raw, "b")
			require.Len(t, got, 2, "%s, %d bytes", name, n)
			require.True(t, got[0] == line+ending+"tail", "%s, %d bytes: first part differs", name, n)
			require.True(t, got[1] == line, "%s, %d bytes: second part differs", name, n)
		}
	}
}

func TestMultipartReaderLongLineThatLooksLikeABoundary(t *testing.T) {
	// A line that starts with the boundary but is longer than the buffer is
	// body text.
	long := "--b" + strings.Repeat(" ", 3*multipartBufSize) + "x"
	got := splitParts(t, "--b\r\n\r\none\r\n"+long+"\r\n--b--", "b")
	require.Len(t, got, 1)
	require.True(t, got[0] == "one\r\n"+long)
}

func TestMultipartReaderSkipsPartWithBigHeader(t *testing.T) {
	raw := "--b\r\n" + strings.Repeat("X: y\r\n", maxHeaderFields+1) + "\r\nbody one\r\n--b\r\n\r\nbody two\r\n--b--"
	require.Equal(t, []string{"body two"}, splitParts(t, raw, "b"))

	// The boundary comes right after the fields that are over the limit.
	raw = "--b\r\n" + strings.Repeat("X: y\r\n", maxHeaderFields+1) + "--b\r\n\r\nbody two\r\n--b--"
	require.Equal(t, []string{"body two"}, splitParts(t, raw, "b"))

	// The input ends inside a header that is too big.
	raw = "--b\r\n" + strings.Repeat("X: y\r\n", maxHeaderFields+1)
	require.Empty(t, splitParts(t, raw, "b"))
}

func TestMultipartReaderReadFailure(t *testing.T) {
	boom := errors.New("connection reset")
	r := io.MultiReader(strings.NewReader("--b\r\n\r\none\r\n--b\r\n\r\ntw"), iotest.ErrReader(boom))
	mr := newMultipartReader(r, "b")

	_, body, _, ok := mr.next()
	require.True(t, ok)
	b, err := io.ReadAll(body)
	require.NoError(t, err)
	require.Equal(t, "one", string(b))

	_, body, _, ok = mr.next()
	require.True(t, ok)
	b, err = io.ReadAll(body)
	require.ErrorIs(t, err, boom)
	require.Equal(t, "tw", string(b))

	// The reader stops. It does not loop on the error.
	_, _, _, ok = mr.next()
	require.False(t, ok)
}

// readHeaderFrom reads a header block and returns it with what is left.
func readHeaderFrom(raw string) (get func(string) string, count int, tooBig bool, rest string) {
	br := bufio.NewReader(strings.NewReader(raw))
	h, tooBig := readHeader(newLineReader(br))
	left, _ := io.ReadAll(br)
	return h.Get, h.Len(), tooBig, string(left)
}

func TestReadHeader(t *testing.T) {
	get, count, tooBig, rest := readHeaderFrom(
		"Subject: one\r\n" +
			"content-type:   text/plain;\r\n" +
			"\tcharset=utf-8\r\n" +
			"X-Empty:\r\n" +
			"X-Spaced : value  \r\n" +
			"\r\n" +
			"body: not a header\r\n")
	require.False(t, tooBig)
	require.Equal(t, 4, count)
	require.Equal(t, "one", get("Subject"))
	require.Equal(t, "text/plain; charset=utf-8", get("Content-Type"))
	require.Equal(t, "", get("X-Empty"))
	require.Equal(t, "value", get("X-Spaced"))
	require.Equal(t, "body: not a header\r\n", rest)
}

func TestReadHeaderSkipsBadLines(t *testing.T) {
	get, count, tooBig, rest := readHeaderFrom(
		" starts with a space\r\n" +
			"From sender@example.test Sat Jan  1 00:00:00 2000\r\n" +
			"no colon at all\r\n" +
			"A: 1\r\n" +
			"bad name: x\r\n" +
			" folded line of the bad field\r\n" +
			": no name\r\n" +
			"B: 2\r\n" +
			"\r\n" +
			"rest")
	require.False(t, tooBig)
	require.Equal(t, 2, count)
	require.Equal(t, "1", get("A"))
	require.Equal(t, "2", get("B"))
	require.Equal(t, "rest", rest)
}

func TestReadHeaderEndsAtTheEndOfTheInput(t *testing.T) {
	get, count, tooBig, rest := readHeaderFrom("A: 1\r\nB: 2")
	require.False(t, tooBig)
	require.Equal(t, 2, count)
	require.Equal(t, "2", get("B"))
	require.Empty(t, rest)

	_, count, tooBig, _ = readHeaderFrom("")
	require.False(t, tooBig)
	require.Zero(t, count)
}

func TestReadHeaderKeepsRepeatedFieldsInOrder(t *testing.T) {
	get, count, _, _ := readHeaderFrom("Received: first\r\nreceived: second\r\n\r\n")
	require.Equal(t, 2, count)
	require.Equal(t, "first", get("Received"))
}

func TestReadHeaderLimits(t *testing.T) {
	// Too many fields: the first maxHeaderFields are kept.
	raw := strings.Repeat("X: y\r\n", maxHeaderFields+5) + "\r\nbody"
	_, count, tooBig, _ := readHeaderFrom(raw)
	require.True(t, tooBig)
	require.Equal(t, maxHeaderFields, count)

	// Exactly the limit is fine.
	_, _, tooBig, rest := readHeaderFrom(strings.Repeat("X: y\r\n", maxHeaderFields) + "\r\nbody")
	require.False(t, tooBig)
	require.Equal(t, "body", rest)

	// Too many bytes in a few long lines.
	line := "X: " + strings.Repeat("a", 100000) + "\r\n"
	_, _, tooBig, _ = readHeaderFrom(strings.Repeat(line, 12) + "\r\nbody")
	require.True(t, tooBig)

	// A line that fills the limit exactly (with its line ending) is fine. One
	// byte more is too big.
	_, _, tooBig, rest = readHeaderFrom("X: " + strings.Repeat("a", maxHeaderBytes-1-3) + "\r\n\r\nbody")
	require.False(t, tooBig)
	require.Equal(t, "body", rest)
	_, _, tooBig, _ = readHeaderFrom("X: " + strings.Repeat("a", maxHeaderBytes-3) + "\r\n\r\nbody")
	require.True(t, tooBig)

	// One line that is too long. The reader stops inside the line.
	_, _, tooBig, rest = readHeaderFrom("X: " + strings.Repeat("a", maxHeaderBytes+10) + "\r\nY: z\r\n\r\nbody")
	require.True(t, tooBig)
	require.Contains(t, rest, "Y: z")
}
