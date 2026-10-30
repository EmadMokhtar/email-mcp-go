// Package errtype names the Go types of an error chain for the log.
//
// The text of an error can hold data that must not be logged, such as text
// from a mail server, an address or a token. Log the Go type names that Chain
// returns, never the text of the error.
package errtype

import (
	"errors"
	"fmt"
	"strings"
)

// maxDepth is the most types that Chain names.
const maxDepth = 6

// Chain returns the Go types of an error and of the errors it wraps, for
// example "*fmt.wrapError>*errors.errorString". It follows errors.Unwrap and
// names at most six types. A type name holds no text from the error itself. It
// returns "" for nil.
func Chain(err error) string {
	var types []string
	for i := 0; err != nil && i < maxDepth; i++ {
		types = append(types, fmt.Sprintf("%T", err))
		err = errors.Unwrap(err)
	}
	return strings.Join(types, ">")
}
