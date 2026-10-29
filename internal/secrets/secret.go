// Package secrets holds the encryption primitives of email-mcp: a Secret type
// that never prints its value, AES-256-GCM sealing with bound context, and the
// master-key providers that protect the keys stored in the database.
package secrets

import (
	"fmt"
	"io"
	"log/slog"
)

// redacted is the text that stands in for every Secret value.
const redacted = "[redacted]"

// Secret is a string that must not appear in logs, errors or output.
//
// A Secret shows "[redacted]" in these places:
//   - fmt, with every verb except %p (see Format);
//   - log/slog, as an attribute (see LogValue);
//   - encoding/json, and so also the log/slog JSON handler, when the Secret is
//     inside a struct, slice or map (see MarshalText).
//
// The only way to get the real value is Reveal, so every place that uses the
// value is easy to find in code review. Code that must write the real value
// to a file or to a network connection has to call Reveal.
//
// Reading is not affected. Decoding text into a Secret, for example from a
// JSON config file, keeps the real value.
//
// Three things are not covered:
//   - A Secret used as a map key. The classic encoding/json writes the raw
//     text of a map key whose type is a string type, and it does not call
//     MarshalText. (Checked with Go 1.24, and with Go 1.27 when built with
//     GOEXPERIMENT=nojsonv2. The default encoding/json of Go 1.27 does call
//     MarshalText.) Do not use a Secret as a map key.
//   - %p with a Secret that is not a pointer. See Format.
//   - A Secret in an unexported field of a struct. fmt cannot call a method of
//     an unexported field, so %v and %+v of the struct print the raw value. The
//     log/slog text handler prints a struct with fmt, so it shows the raw value
//     too. (The JSON handler leaves unexported fields out.) Never print or log
//     a struct that holds a Secret in an unexported field; log the fields that
//     are safe one by one. (Checked with Go 1.27.)
type Secret string

// String returns "[redacted]".
func (Secret) String() string { return redacted }

// LogValue makes log/slog log "[redacted]" instead of the value.
//
// slog calls LogValue only for the attribute itself. For a Secret inside a
// struct, slice or map, the JSON handler calls encoding/json, which uses
// MarshalText.
func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalText makes encoding/json write "[redacted]" for a Secret that is
// inside a struct, slice or map. Without it, encoding/json writes the
// underlying string, and neither String nor Format is used.
//
// There is no UnmarshalText on purpose. Decoding into a Secret must keep the
// real value.
func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// Format makes the fmt verbs print "[redacted]".
//
// String alone is not enough. fmt does not call String for %#v, and for a verb
// that does not fit a string (such as %d) it prints the raw value and calls no
// method. fmt looks for Format before both, so Format covers them.
//
// One verb is not covered: %p with a Secret that is not a pointer. fmt handles
// %p before it looks for Format, and its error text for the wrong verb
// contains the raw value. go vet does not report this, because it accepts
// every verb for a type that has a Format method. Do not use %p with a Secret.
func (Secret) Format(f fmt.State, _ rune) {
	// A write error to a fmt buffer cannot be reported to the caller of Sprintf.
	_, _ = io.WriteString(f, redacted)
}

// Reveal returns the real value. Use it only where the value is needed, for
// example to log in to a mail server.
func (s Secret) Reveal() string { return string(s) }
