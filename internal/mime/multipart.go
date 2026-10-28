package mime

import (
	"bufio"
	"bytes"
	"io"
	stdtextproto "net/textproto"

	"github.com/emersion/go-message/textproto"
)

// This file reads header blocks and multipart bodies. It does not use the
// multipart reader of the library, because that reader has no limit on the
// size of a part header, and it stops at the first line of a header that is
// not valid. Here the work and the memory of a header are bounded, and a bad
// line is skipped.

// multipartBufSize is the size of the read buffer of a multipart body. A line
// that is longer than this is read in pieces.
const multipartBufSize = 16 << 10

// lineReader reads lines from a buffered reader, one piece at a time. It
// never holds more than one buffer of a line, so a very long line costs no
// extra memory.
type lineReader struct {
	br *bufio.Reader
	// atStart is true when the next byte is the first byte of a line.
	atStart bool
	// keep is a copy of a piece of a line, see chunk.
	keep []byte
}

func newLineReader(br *bufio.Reader) *lineReader {
	return &lineReader{br: br, atStart: true}
}

// chunk reads up to one line. data is the text of the line without its line
// ending. It points into the buffer and is valid until the next read.
//
// eol is the line ending ("\n" or "\r\n") that ended data. It is empty when
// the line is longer than the buffer (the rest comes with the next chunk) or
// when the input ended. start is true when data begins a line. err is io.EOF
// when the input has ended (data may still hold the last, unfinished line),
// or another error when reading failed.
func (l *lineReader) chunk() (data []byte, eol string, start bool, err error) {
	start = l.atStart
	data, err = l.br.ReadSlice('\n')
	switch err {
	case nil:
		eol = "\n"
		data = data[:len(data)-1]
		if n := len(data); n > 0 && data[n-1] == '\r' {
			eol = "\r\n"
			data = data[:n-1]
		}
		l.atStart = true
	case bufio.ErrBufferFull:
		err = nil
		l.atStart = false
		// The buffer may end between the "\r" and the "\n" of a line ending.
		if n := len(data); n > 0 && data[n-1] == '\r' {
			// Looking at the next byte reads into the buffer again, and that
			// overwrites data. So keep a copy of it.
			l.keep = append(l.keep[:0], data...)
			data = l.keep
			if next, peekErr := l.br.Peek(1); peekErr == nil && next[0] == '\n' {
				_, _ = l.br.Discard(1)
				data = data[:n-1]
				eol = "\r\n"
				l.atStart = true
			}
		}
	default:
		l.atStart = false
	}
	return data, eol, start, err
}

// readLine reads a whole line, without its line ending, up to limit bytes.
// tooLong is true when the line is longer than that. The rest of the line is
// then not read. The result may point into the buffer.
func (l *lineReader) readLine(limit int) (line []byte, tooLong bool, err error) {
	data, eol, _, err := l.chunk()
	if eol != "" || err != nil {
		return data, len(data) > limit, err
	}
	// The line is longer than the buffer. Collect the pieces.
	line = append([]byte(nil), data...)
	for len(line) <= limit {
		data, eol, _, err = l.chunk()
		line = append(line, data...)
		if eol != "" || err != nil {
			break
		}
	}
	return line, len(line) > limit, err
}

// readHeader reads a header block: fields up to the first empty line, or the
// end of the input. A line that is not a field (no colon, or a bad field
// name, like the "From " line of a mbox file) is skipped, and so are its
// folded lines. Fields with the same name are kept in the order they were
// read.
//
// The size is limited to maxHeaderBytes and maxHeaderFields. When a limit is
// reached, tooBig is true, the fields read so far are returned, and the
// reader stays where it stopped.
func readHeader(l *lineReader) (header textproto.Header, tooBig bool) {
	fields := map[string][]string{}
	var (
		budget  = maxHeaderBytes
		count   int
		key     string
		value   []byte
		pending bool // key and value hold a field that is not stored yet
	)
	store := func() {
		if pending {
			fields[key] = append(fields[key], string(bytes.TrimSpace(value)))
			pending = false
		}
	}
	finish := func(big bool) (textproto.Header, bool) {
		store()
		return textproto.HeaderFromMap(fields), big
	}

	for {
		// A line and its line ending must fit in what is left.
		line, tooLong, err := l.readLine(max(budget-1, 0))
		if tooLong {
			return finish(true)
		}
		budget -= len(line) + 1
		switch {
		case len(line) == 0:
			// The empty line ends the header. So does the end of the input.
			return finish(false)
		case line[0] == ' ' || line[0] == '\t':
			// A folded line belongs to the field before it. If that field
			// was skipped, so is this line.
			if pending {
				value = append(append(value, ' '), bytes.TrimSpace(line)...)
			}
		default:
			store()
			if name, rest, ok := splitField(line); ok {
				count++
				if count > maxHeaderFields {
					return finish(true)
				}
				key, value, pending = name, append(value[:0], rest...), true
			}
		}
		if err != nil {
			return finish(false)
		}
	}
}

// splitField splits a header line at the first colon. ok is false when the
// field name is empty or has a character that a field name may not have.
func splitField(line []byte) (name string, value []byte, ok bool) {
	i := bytes.IndexByte(line, ':')
	if i <= 0 {
		return "", nil, false
	}
	raw := bytes.TrimRight(line[:i], " \t")
	if len(raw) == 0 {
		return "", nil, false
	}
	for _, c := range raw {
		if c < '!' || c > '~' {
			return "", nil, false
		}
	}
	return stdtextproto.CanonicalMIMEHeaderKey(string(raw)), line[i+1:], true
}

// multipartReader splits the body of a multipart part into its parts.
type multipartReader struct {
	lines *lineReader
	// dash is "--" and the boundary.
	dash []byte
	// cur is the part that was returned last. At first it is the text before
	// the first boundary (the preamble), which is not a part.
	cur *partBody
	// final is true when no part can follow cur: its end was the closing
	// boundary, or the input ended.
	final bool
}

func newMultipartReader(r io.Reader, boundary string) *multipartReader {
	m := &multipartReader{
		lines: newLineReader(bufio.NewReaderSize(r, multipartBufSize)),
		dash:  []byte("--" + boundary),
	}
	m.cur = &partBody{m: m}
	return m
}

// next returns the header and the body of the next part. ok is false when
// there are no more parts. tooBig is true when the header of the part is over
// a limit; the part has to be skipped, and its body is not usable. The body
// is only valid until the next call.
func (m *multipartReader) next() (header textproto.Header, body io.Reader, tooBig, ok bool) {
	// Read to the end of the previous part, up to its boundary.
	_, _ = io.Copy(io.Discard, m.cur)
	if m.final {
		return header, nil, false, false
	}
	header, tooBig = readHeader(m.lines)
	m.cur = &partBody{m: m}
	return header, m.cur, tooBig, true
}

// delimiter reports whether line is a boundary line, and whether it is the
// closing one. A boundary line is the boundary after two dashes, with spaces
// or tabs after it. The closing one has two more dashes. A line that only
// starts with the boundary (a longer boundary) is not one.
func (m *multipartReader) delimiter(line []byte) (isDelimiter, closing bool) {
	rest, ok := bytes.CutPrefix(line, m.dash)
	if !ok {
		return false, false
	}
	if after, hasDashes := bytes.CutPrefix(rest, []byte("--")); hasDashes && onlyBlanks(after) {
		return true, true
	}
	return onlyBlanks(rest), false
}

func onlyBlanks(b []byte) bool {
	for _, c := range b {
		if c != ' ' && c != '\t' {
			return false
		}
	}
	return true
}

// partBody is the body of one part. It ends at the next boundary line, or at
// the end of the input. The line ending before a boundary line belongs to the
// boundary, so it is not part of the body.
type partBody struct {
	m *multipartReader
	// buf has the data that was read from the input. The part of it from off
	// on is ready to be read. The array is used again for the next piece.
	buf []byte
	off int
	// held is the line ending of the last line. It is kept back until it is
	// known that the next line is not a boundary.
	held string
	err  error
}

func (p *partBody) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	for p.off == len(p.buf) {
		if p.err != nil {
			return 0, p.err
		}
		p.fill()
	}
	n := copy(b, p.buf[p.off:])
	p.off += n
	return n, nil
}

// fill reads the next piece of the body into buf, or finds the end.
func (p *partBody) fill() {
	m := p.m
	data, eol, start, err := m.lines.chunk()

	// A line can be a boundary only if it begins a line and is whole.
	if start && (eol != "" || err == io.EOF) {
		if isDelimiter, closing := m.delimiter(data); isDelimiter {
			p.err = io.EOF
			m.final = closing || err == io.EOF
			return
		}
	}

	p.buf = append(append(p.buf[:0], p.held...), data...)
	p.off = 0
	p.held = eol
	if err != nil {
		// The input ended without a boundary. The line ending is data.
		p.buf = append(p.buf, p.held...)
		p.held = ""
		p.err = err
		m.final = true
	}
}
