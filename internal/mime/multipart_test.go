package mime

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/require"
)

// splitParts runs the multipart reader over raw and returns the body of each
// part that is not skipped.
func splitParts(t *testing.T, raw, boundary string) []string {
	t.Helper()
	return splitPartsFrom(t, strings.NewReader(raw), boundary)
}

func splitPartsFrom(t *testing.T, r io.Reader, boundary string) []string {
	t.Helper()
	var bodies []string
	mr := newMultipartReader(r, boundary)
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

// readers are ways to deliver the same input. The reader must give the same
// parts however the data arrives.
var readers = map[string]func(string) io.Reader{
	"whole":        func(s string) io.Reader { return strings.NewReader(s) },
	"one byte":     func(s string) io.Reader { return iotest.OneByteReader(strings.NewReader(s)) },
	"half":         func(s string) io.Reader { return iotest.HalfReader(strings.NewReader(s)) },
	"data and eof": func(s string) io.Reader { return iotest.DataErrReader(strings.NewReader(s)) },
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
		{
			name: "opening boundary with a line ending at the very end is not a part",
			raw:  "--b\r\n\r\none\r\n--b\r\n",
			want: []string{"one"},
		},
		{
			name: "mixed line endings",
			raw:  "--b\n\none\r\n--b\r\n\ntwo\n--b--\r\n",
			want: []string{"one", "two"},
		},
		{
			name: "header ends at a boundary line",
			raw:  "--b\r\nX: y\r\n--b\r\nX: z\r\n\r\nbody\r\n--b--",
			want: []string{"", "body"},
		},
		{
			name: "boundary right after a boundary",
			raw:  "--b\r\n--b\r\n\r\nbody\r\n--b--",
			want: []string{"", "body"},
		},
		{
			name: "header ends at the closing boundary",
			raw:  "--b\r\n\r\none\r\n--b\r\nX: y\r\n--b--\r\n--b\r\n\r\nnot a part",
			want: []string{"one", ""},
		},
		{
			name: "text that only looks like a closing boundary",
			raw:  "--b\r\n\r\none\r\n--b--junk\r\ntwo\r\n--b--",
			want: []string{"one\r\n--b--junk\r\ntwo"},
		},
		{
			name: "carriage return before the end",
			raw:  "--b\r\n\r\none\r",
			want: []string{"one\r"},
		},
		{
			name: "boundary with a plus sign and a dot",
			raw:  "--b\r\n\r\nx\r\n--b--",
			want: []string{"x"},
		},
	}
	for _, tc := range tests {
		for name, wrap := range readers {
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				require.Equal(t, tc.want, splitPartsFrom(t, wrap(tc.raw), "b"))
			})
		}
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
	h, end := readHeader(newLineReader(br), nil)
	left, _ := io.ReadAll(br)
	return h.Get, h.Len(), end == headerTooBig, string(left)
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

// Every line that the reader sees is one of these, with a random line ending.
var fuzzLines = []string{
	"", "a", "bb", "a\rb", "--b", "--b--", "--b ", "--b \t", "--b-", "--bx", "--b--x", "--b  --",
	" --b", "x--b", "--", "-", "X: y", "--bb", "--b\r",
}

// referenceSplit is a plain, line by line version of the multipart reader. It
// is slow and simple, so it is easy to believe. Every part must start with an
// empty line or a boundary line (no header fields), so the header rules do not
// matter here.
func referenceSplit(raw, boundary string) []string {
	dash := "--" + boundary
	isDelimiter := func(line string) (bool, bool) {
		rest, ok := strings.CutPrefix(line, dash)
		if !ok {
			return false, false
		}
		if after, ok := strings.CutPrefix(rest, "--"); ok && strings.Trim(after, " \t") == "" {
			return true, true
		}
		return strings.Trim(rest, " \t") == "", false
	}
	type line struct{ text, eol string }
	var lines []line
	for rest := raw; rest != ""; {
		i := strings.IndexByte(rest, '\n')
		if i < 0 {
			lines = append(lines, line{rest, ""})
			break
		}
		text, eol := rest[:i], "\n"
		if strings.HasSuffix(text, "\r") {
			text, eol = text[:len(text)-1], "\r\n"
		}
		lines = append(lines, line{text, eol})
		rest = rest[i+1:]
	}

	var bodies []string
	i := 0
	for i < len(lines) {
		if d, _ := isDelimiter(lines[i].text); d {
			break
		}
		i++
	}
	for i < len(lines) {
		if _, closing := isDelimiter(lines[i].text); closing {
			break
		}
		i++ // the opening boundary line
		if i >= len(lines) {
			break
		}
		// The header runs to an empty line or a boundary line.
		for i < len(lines) && lines[i].text != "" {
			if d, _ := isDelimiter(lines[i].text); d {
				break
			}
			i++
		}
		if i < len(lines) && lines[i].text == "" {
			i++
		}
		var body strings.Builder
		for i < len(lines) {
			if d, _ := isDelimiter(lines[i].text); d {
				break
			}
			body.WriteString(lines[i].text)
			// The line ending before a boundary line belongs to the boundary.
			if i+1 < len(lines) {
				if d, _ := isDelimiter(lines[i+1].text); !d {
					body.WriteString(lines[i].eol)
				}
			} else {
				body.WriteString(lines[i].eol)
			}
			i++
		}
		bodies = append(bodies, body.String())
	}
	return bodies
}

func TestMultipartReaderAgreesWithTheReference(t *testing.T) {
	// A fixed seed: the test must give the same inputs every time.
	rng := rand.New(rand.NewSource(1)) //nolint:gosec // not used for security
	for n := 0; n < 4000; n++ {
		var b strings.Builder
		for k := rng.Intn(14); k >= 0; k-- {
			text := fuzzLines[rng.Intn(len(fuzzLines))]
			if rng.Intn(40) == 0 {
				// A line that ends near the end of the read buffer.
				text = strings.Repeat("a", multipartBufSize-3+rng.Intn(6))
			}
			b.WriteString(text)
			if k > 0 || rng.Intn(4) > 0 {
				if rng.Intn(2) == 0 {
					b.WriteString("\r\n")
				} else {
					b.WriteString("\n")
				}
			}
		}
		raw := b.String()
		want := referenceSplit(raw, "b")
		for name, wrap := range readers {
			if name == "one byte" && len(raw) > 4000 {
				continue
			}
			got := splitPartsFrom(t, wrap(raw), "b")
			if len(got) != len(want) {
				require.Failf(t, "parts differ", "%s, input %q: got %d parts, want %d", name, raw, len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					require.Failf(t, "part differs", "%s, input %q, part %d: got %q, want %q", name, raw, i, got[i], want[i])
				}
			}
		}
	}
}

func TestMultipartReaderBoundaryAtEveryOffsetOfTheBuffer(t *testing.T) {
	for _, ending := range []string{"\r\n", "\n"} {
		for k := multipartBufSize - 90; k < multipartBufSize+90; k++ {
			body := strings.Repeat("a", k)
			// A real boundary after k bytes, and a line that only looks like one.
			raw := "--b" + ending + ending + body + ending + "--bx" + ending + body + ending + "--b" + ending + ending + "next" + ending + "--b--"
			got := splitParts(t, raw, "b")
			require.Len(t, got, 2, "%q ending, %d bytes", ending, k)
			want := body + ending + "--bx" + ending + body
			require.True(t, got[0] == want, "%q ending, %d bytes: first part differs", ending, k)
			require.Equal(t, "next", got[1])
		}
	}
}

// callCounter counts the Read calls that are made on it.
type callCounter struct {
	r     io.Reader
	calls int
}

func (c *callCounter) Read(p []byte) (int, error) {
	c.calls++
	return c.r.Read(p)
}

// The work for a body must not depend on the number of lines in it. A body of
// line endings only has one line for each byte.
func TestMultipartReaderMovesBodiesInLargeChunks(t *testing.T) {
	const depth = 8
	const size = 8 << 20
	var head strings.Builder
	for k := 0; k < depth; k++ {
		fmt.Fprintf(&head, "--b%d\r\n\r\n", k) // a part with no header fields
	}
	src := io.MultiReader(strings.NewReader(head.String()), strings.NewReader(strings.Repeat("\n", size)))

	counters := make([]*callCounter, depth)
	for k := 0; k < depth; k++ {
		_, body, _, ok := newMultipartReader(src, fmt.Sprintf("b%d", k)).next()
		require.True(t, ok)
		counters[k] = &callCounter{r: body}
		src = counters[k]
	}
	n, err := io.Copy(io.Discard, src)
	require.NoError(t, err)
	require.Equal(t, int64(size), n)
	for k, c := range counters {
		require.Less(t, c.calls, size/1024, "level %d: reads of the body", k)
	}
}

func TestMultipartBoundaryThatIsTooLongIsNotUsed(t *testing.T) {
	boundary := strings.Repeat("b", 5000)
	raw := "Content-Type: multipart/mixed; boundary=" + boundary + "\r\n\r\n--" + boundary + "\r\nContent-Type: text/plain\r\n\r\ntext\r\n--" + boundary + "--"
	p, err := ParseMessage(strings.NewReader(raw), 0)
	require.NoError(t, err)
	require.Empty(t, p.Text)
}
