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
// Printing a Secret with fmt (any verb), or logging it with log/slog, shows
// "[redacted]". The only way to get the real value is Reveal, so every place
// that uses the value is easy to find in code review.
type Secret string

// String returns "[redacted]".
func (Secret) String() string { return redacted }

// LogValue makes log/slog log "[redacted]" instead of the value.
func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// Format makes every fmt verb print "[redacted]".
//
// String alone is not enough. fmt does not call String for %#v, and for a verb
// that does not fit a string (such as %d) it prints the raw value and does not
// call any method. Format is checked first for every verb, so it covers all of
// them.
func (Secret) Format(f fmt.State, _ rune) {
	// A write error to a fmt buffer cannot be reported to the caller of Sprintf.
	_, _ = io.WriteString(f, redacted)
}

// Reveal returns the real value. Use it only where the value is needed, for
// example to log in to a mail server.
func (s Secret) Reveal() string { return string(s) }
