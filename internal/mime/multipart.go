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
//
// A part body is not read line by line. The reader searches the buffered data
// for a line ending and the boundary with a fast search, and gives out large
// pieces. So the work for a body does not depend on how many lines it has,
// also when multipart parts are nested and each level passes the body up.

// multipartBufSize is the size of the read buffer of a multipart body. A line
// that is longer than this is read in pieces.
const multipartBufSize = 16 << 10

// lineReader reads lines from a buffered reader, one piece at a time. It
// never holds more than one buffer of a line, so a very long line costs no
// extra memory.
type lineReader struct {
	br *bufio.Reader
	// atStart is true when the next byte is the first byte of a line. It is
	// kept up to date by chunk and readLine only. The body of a part is read
	// without the lineReader, but the header comes before it, so atStart is
	// right when a body starts.
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
// when the input ended. err is io.EOF when the input has ended (data may still
// hold the last, unfinished line), or another error when reading failed.
func (l *lineReader) chunk() (data []byte, eol string, err error) {
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
	return data, eol, err
}

// readLine reads a whole line, without its line ending, up to limit bytes.
// tooLong is true when the line is longer than that. The rest of the line is
// then not read. The result may point into the buffer.
func (l *lineReader) readLine(limit int) (line []byte, tooLong bool, err error) {
	data, eol, err := l.chunk()
	if eol != "" || err != nil {
		return data, len(data) > limit, err
	}
	// The line is longer than the buffer. Collect the pieces.
	line = append([]byte(nil), data...)
	for len(line) <= limit {
		data, eol, err = l.chunk()
		line = append(line, data...)
		if eol != "" || err != nil {
			break
		}
	}
	return line, len(line) > limit, err
}

// headerEnd says why readHeader stopped.
type headerEnd int

const (
	// headerBlank: the empty line, or the end of the input.
	headerBlank headerEnd = iota
	// headerTooBig: a limit was reached.
	headerTooBig
	// headerBoundary: a boundary line came before the empty line.
	headerBoundary
	// headerClosing: a closing boundary line came before the empty line.
	headerClosing
)

// readHeader reads a header block: fields up to the first empty line, or the
// end of the input. A line that is not a field (no colon, or a bad field
// name, like the "From " line of a mbox file) is skipped, and so are its
// folded lines. Fields with the same name are kept in the order they were
// read.
//
// The size is limited to maxHeaderBytes and maxHeaderFields. When a limit is
// reached, the end is headerTooBig, the fields read so far are returned, and
// the reader stays where it stopped.
//
// For the header of a part, delimiter tells if a line is a boundary line (see
// multipartReader.delimiter). Such a line ends the header, because the part
// has no more lines. The line is read, and the end says what it was. For the
// header of a message, delimiter is nil.
func readHeader(l *lineReader, delimiter func(line []byte) (isDelimiter, closing bool)) (header textproto.Header, end headerEnd) {
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
	finish := func(end headerEnd) (textproto.Header, headerEnd) {
		store()
		return textproto.HeaderFromMap(fields), end
	}

	for {
		// A line and its line ending must fit in what is left.
		line, tooLong, err := l.readLine(max(budget-1, 0))
		if tooLong {
			return finish(headerTooBig)
		}
		budget -= len(line) + 1
		if delimiter != nil {
			if isDelimiter, closing := delimiter(line); isDelimiter {
				if closing {
					return finish(headerClosing)
				}
				return finish(headerBoundary)
			}
		}
		switch {
		case len(line) == 0:
			// The empty line ends the header. So does the end of the input.
			return finish(headerBlank)
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
					return finish(headerTooBig)
				}
				key, value, pending = name, append(value[:0], rest...), true
			}
		}
		if err != nil {
			return finish(headerBlank)
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
	// dash is "--" and the boundary. nlDash is a line ending and dash.
	dash, nlDash []byte
	// cur is the part that was returned last. At first it is the text before
	// the first boundary (the preamble), which is not a part.
	cur *partBody
	// final is true when no part can follow cur: its end was the closing
	// boundary, or the input ended.
	final bool
}

func newMultipartReader(r io.Reader, boundary string) *multipartReader {
	dash := []byte("--" + boundary)
	m := &multipartReader{
		lines:  newLineReader(bufio.NewReaderSize(r, multipartBufSize)),
		dash:   dash,
		nlDash: append([]byte("\n"), dash...),
	}
	m.cur = &partBody{m: m, atStart: true}
	return m
}

// next returns the header and the body of the next part. ok is false when
// there are no more parts. tooBig is true when the header of the part is over
// a limit; the part has to be skipped, and its body is not usable. The body
// is only valid until the next call.
func (m *multipartReader) next() (header textproto.Header, body io.Reader, tooBig, ok bool) {
	// Read to the end of the previous part, up to its boundary.
	_, _ = io.Copy(io.Discard, m.cur)
	if !m.final {
		if _, err := m.lines.br.Peek(1); err != nil {
			// A boundary line and then nothing: there is no part after it.
			m.final = true
		}
	}
	if m.final {
		return header, nil, false, false
	}
	header, end := readHeader(m.lines, m.delimiter)
	m.cur = &partBody{m: m, atStart: m.lines.atStart}
	if end == headerBoundary || end == headerClosing {
		// The header ran into a boundary line. The part has no body, and
		// that line is already read.
		m.cur.err = io.EOF
		m.final = end == headerClosing
	}
	return header, m.cur, end == headerTooBig, true
}

// delimiter reports whether line is a boundary line, and whether it is the
// closing one. line has no line ending. A boundary line is the boundary after
// two dashes, with spaces or tabs after it. The closing one has two more
// dashes. A line that only starts with the boundary (a longer boundary, or
// other text after it) is not one.
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

// bodyEnd describes the boundary line that ends a part body.
type bodyEnd struct {
	// closing is true for the closing boundary.
	closing bool
	// drop is the number of bytes after the body to skip: the line ending
	// before the boundary line, the line, and its line ending.
	drop int
}

// scanBody looks at the data that is buffered (peek) and decides what part of
// it is body. It does not read. This is how the multipart reader of the
// standard library works too: it looks for a line ending and the boundary
// with a fast search, and does not go through the lines one by one.
//
//   - ready is the number of bytes at the start of peek that are body and can
//     be given out now.
//   - end is not nil when a boundary line follows the ready bytes.
//   - more is true when nothing can be given out and nothing is decided: more
//     data is needed.
//
// atStart says that peek begins at the start of a line, and the body has not
// started to be read. eof says that no more data comes after peek. full says
// that peek fills the buffer, so a line that starts at its start and is not
// complete in it is too long to be a boundary line.
//
// Bytes that could be the start of a line ending and a boundary are not
// ready until more data shows what they are.
func (m *multipartReader) scanBody(peek []byte, atStart, eof, full bool) (ready int, end *bodyEnd, more bool) {
	if atStart {
		if bytes.HasPrefix(peek, m.dash) {
			isBoundary, closing, lineEnd, needMore := m.boundaryLine(peek, 0, eof, full)
			if needMore {
				return 0, nil, true
			}
			if isBoundary {
				return 0, &bodyEnd{closing: closing, drop: lineEnd}, false
			}
		} else if !eof && bytes.HasPrefix(m.dash, peek) {
			return 0, nil, true // may still become the boundary
		}
	}

	for from := 0; ; {
		i := bytes.Index(peek[from:], m.nlDash)
		if i < 0 {
			break
		}
		i += from
		// The line ending before the boundary line is not body. It can be
		// "\r\n".
		bodyLen := i
		if i > 0 && peek[i-1] == '\r' {
			bodyLen = i - 1
		}
		// If body comes before the line, giving it out makes room in the
		// buffer. So the line can only be too long for the buffer when no body
		// comes before it.
		isBoundary, closing, lineEnd, needMore := m.boundaryLine(peek, i+1, eof, full && bodyLen == 0)
		switch {
		case needMore:
			return bodyLen, nil, bodyLen == 0
		case isBoundary:
			return bodyLen, &bodyEnd{closing: closing, drop: lineEnd - bodyLen}, false
		}
		from = i + 1 // the text is body. Go on after its line ending.
	}

	if eof {
		return len(peek), nil, false
	}
	// No boundary line in peek. Keep back the end if it could be the start of
	// one: a "\r", or a line ending and the first characters of the boundary.
	n := len(peek)
	if j := bytes.LastIndexByte(peek, '\n'); j >= 0 && bytes.HasPrefix(m.nlDash, peek[j:]) {
		n = j
	}
	if n > 0 && peek[n-1] == '\r' {
		n--
	}
	return n, nil, n == 0
}

// boundaryLine looks at the line that starts at peek[start:] and begins with
// the boundary dashes. lineEnd is the index after its line ending. A line
// that is not complete in peek makes needMore true, unless peek is full. Then
// the line is too long to be a boundary line.
func (m *multipartReader) boundaryLine(peek []byte, start int, eof, full bool) (isBoundary, closing bool, lineEnd int, needMore bool) {
	j := bytes.IndexByte(peek[start:], '\n')
	var line []byte
	switch {
	case j >= 0:
		lineEnd = start + j + 1
		line = bytes.TrimSuffix(peek[start:lineEnd-1], []byte("\r"))
	case eof:
		lineEnd = len(peek)
		line = peek[start:]
	case full:
		return false, false, 0, false
	default:
		return false, false, 0, true
	}
	isBoundary, closing = m.delimiter(line)
	return isBoundary, closing, lineEnd, false
}

// partBody is the body of one part. It ends at the next boundary line, or at
// the end of the input. The line ending before a boundary line belongs to the
// boundary, so it is not part of the body.
type partBody struct {
	m *multipartReader
	// atStart is true until the first byte is given out, when it is known
	// that the body starts at the start of a line.
	atStart bool
	// ready is the number of buffered bytes that are body and can be given
	// out. They are at the front of the buffer of the reader.
	ready int
	// end is set when a boundary line follows the ready bytes.
	end *bodyEnd
	// readErr is the error of the last try to read more: io.EOF at the end of
	// the input. Data that is buffered is still used.
	readErr error
	err     error
}

func (p *partBody) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	br := p.m.lines.br
	for {
		if p.ready > 0 {
			// The bytes are in the buffer already, so this does not read.
			n, _ := br.Read(b[:min(len(b), p.ready)])
			p.ready -= n
			p.atStart = false
			return n, nil
		}
		switch {
		case p.err != nil:
			return 0, p.err
		case p.end != nil:
			_, _ = br.Discard(p.end.drop)
			p.m.final = p.end.closing
			p.err = io.EOF
		default:
			p.scan()
		}
	}
}

// scan decides what to do with the buffered data, or reads more.
func (p *partBody) scan() {
	m := p.m
	br := m.lines.br
	peek, _ := br.Peek(br.Buffered())
	eof := p.readErr != nil

	ready, end, more := m.scanBody(peek, p.atStart, eof, len(peek) == br.Size())
	switch {
	case more:
		// Peek reads until there is one more byte, or fails.
		_, p.readErr = br.Peek(len(peek) + 1)
	case ready == 0 && end == nil:
		// All is read and used: this is the end of the input.
		p.err = p.readErr
		m.final = true
	default:
		p.ready, p.end = ready, end
	}
}
