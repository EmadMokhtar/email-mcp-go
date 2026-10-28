package mime

import (
	"bytes"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/emersion/go-message/textproto"
)

// This file reads the parameters of header values ("name=value" after a
// semicolon) with rules that are looser than the standard library. It is used
// when the strict reading fails.

// rawParams reads the parameters of a header value. Names are lower case. A
// value in double quotes is unquoted (a backslash escapes the next
// character), and it may hold a semicolon. An unquoted value ends at the next
// semicolon, or, with cutAtSpace, at the first space or tab. When a name
// repeats, the first value is kept.
func rawParams(value string, cutAtSpace bool) map[string]string {
	params := map[string]string{}
	_, rest, _ := strings.Cut(value, ";")
	for _, field := range splitParams(rest) {
		name, val, ok := strings.Cut(field, "=")
		name = strings.ToLower(strings.TrimSpace(name))
		if !ok || name == "" {
			continue
		}
		if _, seen := params[name]; !seen {
			params[name] = unquoteParam(strings.TrimSpace(val), cutAtSpace)
		}
	}
	return params
}

// splitParams splits s at the semicolons that are not inside double quotes.
func splitParams(s string) []string {
	var fields []string
	start, inQuote := 0, false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case inQuote && c == '\\':
			i++ // skip the escaped character
		case c == '"':
			inQuote = !inQuote
		case c == ';' && !inQuote:
			fields = append(fields, s[start:i])
			start = i + 1
		}
	}
	return append(fields, s[start:])
}

func unquoteParam(val string, cutAtSpace bool) string {
	if strings.HasPrefix(val, `"`) {
		var b strings.Builder
		for i := 1; i < len(val); i++ {
			c := val[i]
			if c == '\\' && i+1 < len(val) {
				i++
				c = val[i]
			} else if c == '"' {
				break
			}
			b.WriteByte(c)
		}
		return b.String()
	}
	if cutAtSpace {
		if i := strings.IndexAny(val, " \t"); i >= 0 {
			val = val[:i]
		}
	}
	return val
}

// lenientFilename reads the file name of a part without strict rules: the
// "filename" parameter of the Content-Disposition, or else the "name"
// parameter of the Content-Type. See paramText for how a value is decoded.
// The result is valid UTF-8.
func lenientFilename(h textproto.Header) string {
	if name := paramText(rawParams(h.Get("Content-Disposition"), false), "filename"); name != "" {
		return name
	}
	return paramText(rawParams(h.Get("Content-Type"), false), "name")
}

// paramText returns the text of the parameter called name. It reads RFC 2231
// values (name*=charset'language'text, and the numbered pieces name*0*,
// name*1*, ...), and RFC 2047 words in a plain value. It returns an empty
// string when there is no such parameter. The result is valid UTF-8.
func paramText(params map[string]string, name string) string {
	if text := paramText2231(params, name); text != "" {
		return text
	}
	if value, ok := params[name]; ok {
		return DecodeHeaderValue(value)
	}
	return ""
}

func paramText2231(params map[string]string, name string) string {
	type piece struct {
		value   string
		encoded bool // the value uses %XX escapes
	}
	var pieces []piece
	for i := 0; ; i++ {
		key := name + "*" + strconv.Itoa(i)
		if v, ok := params[key+"*"]; ok {
			pieces = append(pieces, piece{v, true})
		} else if v, ok := params[key]; ok {
			pieces = append(pieces, piece{v, false})
		} else {
			break
		}
	}
	if len(pieces) == 0 {
		v, ok := params[name+"*"]
		if !ok {
			return ""
		}
		pieces = []piece{{v, true}}
	}

	var raw []byte
	label := ""
	for i, p := range pieces {
		v := p.value
		if !p.encoded {
			raw = append(raw, v...)
			continue
		}
		if i == 0 {
			// The first piece starts with the charset and the language.
			if parts := strings.SplitN(v, "'", 3); len(parts) == 3 {
				label, v = parts[0], parts[2]
			}
		}
		raw = appendUnescaped(raw, v)
	}
	text, _ := io.ReadAll(charsetDecoder(label, bytes.NewReader(raw)))
	return strings.ToValidUTF8(string(text), string(utf8.RuneError))
}

// appendUnescaped adds s to dst and replaces each "%XX" with its byte. A "%"
// that is not followed by two hex digits stays as it is.
func appendUnescaped(dst []byte, s string) []byte {
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			dst = append(dst, unhex(s[i+1])<<4|unhex(s[i+2]))
			i += 2
			continue
		}
		dst = append(dst, s[i])
	}
	return dst
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c <= '9':
		return c - '0'
	case c <= 'F':
		return c - 'A' + 10
	default:
		return c - 'a' + 10
	}
}
