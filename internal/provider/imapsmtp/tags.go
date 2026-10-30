package imapsmtp

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
)

// A tag is an IMAP keyword (a flag without a backslash) that the user or the
// agent can see and set. The backend must not let a keyword act as anything
// else. Mail clients use keywords for their own state (for example $Forwarded,
// $label1 and the spam marks Junk and NonJunk), and a flag with a backslash is
// a system flag (\Deleted, \Seen). So there is one rule for what a tag is, and
// every place that makes, lists, sets or searches tags uses it (isTag).

// tagPattern is the form of a tag name: letters, digits, "-" and "_", 1 to 64
// characters. The pattern is anchored, and "$" in Go matches only at the end
// of the text (not before a final line break). So "a\n" is not a tag name.
var tagPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// reservedTags are keywords that spam filters of mail clients set. They are
// not tags. They are compared without regard to case, like all keywords.
var reservedTags = []string{"Junk", "NonJunk", "NotJunk"}

// isTag reports whether a keyword of a message is a tag. It is when the
// keyword has the form of tagPattern and it is not reserved. A keyword that
// starts with "$", and every system flag (they start with a backslash), is
// not a tag, because neither fits the pattern.
func isTag(keyword string) bool {
	return tagPattern.MatchString(keyword) && !isReservedTag(keyword)
}

func isReservedTag(keyword string) bool {
	for _, r := range reservedTags {
		if strings.EqualFold(keyword, r) {
			return true
		}
	}
	return false
}

// checkTagName checks a tag name from a caller. The error wraps
// mail.ErrUnsupported. The name goes into a STORE command later, so a name
// with a backslash or a space must never get that far: "\Deleted" as a tag
// would mark the message for deletion, and the mail client of the user can
// expunge it later.
func checkTagName(name string) error {
	if !tagPattern.MatchString(name) {
		return fmt.Errorf("%w: tag names on this account may only use letters, digits, - and _", mail.ErrUnsupported)
	}
	if isReservedTag(name) {
		return fmt.Errorf("%w: %q is reserved for the spam filters of mail clients and cannot be a tag", mail.ErrUnsupported, name)
	}
	return nil
}

// checkTagNames checks all the names. The first bad name is the error.
func checkTagNames(names []string) error {
	for _, name := range names {
		if err := checkTagName(name); err != nil {
			return err
		}
	}
	return nil
}

// tagFlags returns the names as flags, each name once (names are not case
// sensitive). The names must be checked before.
func tagFlags(names []string) []imap.Flag {
	var out []imap.Flag
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		key := strings.ToLower(name)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, imap.Flag(name))
	}
	return out
}

// tagSupport remembers whether the account lets a client make new keywords,
// which is what tags need. The server says so with the wildcard \* in the
// PERMANENTFLAGS of a mailbox. The answer for the inbox is used for the
// account.
//
// The zero value knows nothing. It is safe for use by many goroutines.
type tagSupport struct {
	mu        sync.Mutex
	known     bool
	supported bool
}

// get returns the remembered answer. known is false when there is none yet.
func (t *tagSupport) get() (supported, known bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.supported, t.known
}

// note remembers what the answer of SELECT or EXAMINE for the inbox says.
//
// The wildcard in the list is a sure yes. A list without it is a sure no only
// when the mailbox was opened for writing (SELECT). A server may report no
// permanent flags at all for a mailbox that is open read-only, because a
// read-only mailbox cannot change (Dovecot does). Then the list says nothing
// about keywords, so the answer stays as it was.
func (t *tagSupport) note(permanent []imap.Flag, readOnly bool) {
	wildcard := false
	for _, f := range permanent {
		if f == imap.FlagWildcard {
			wildcard = true
			break
		}
	}
	if !wildcard && readOnly {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.known, t.supported = true, wildcard
}

// openFolder selects a mailbox, read-only (EXAMINE) or for writing (SELECT).
// When the mailbox is the inbox, it also notes whether the account supports
// tags (see tagSupport). Every code path that opens the inbox goes through
// here, so the answer is usually known before anyone asks.
func (b *Backend) openFolder(c *imapclient.Client, mailbox string, readOnly bool) (*imap.SelectData, error) {
	sel, err := openMailbox(c, mailbox, readOnly)
	if err == nil && strings.EqualFold(mailbox, inboxName) {
		b.tags.note(sel.PermanentFlags, readOnly)
	}
	return sel, err
}

// tagsSupported reports whether the account supports tags. When the answer is
// not known yet, it opens the inbox for writing (SELECT) once to find out. It
// does not use EXAMINE, because the answer of EXAMINE can be empty (see
// tagSupport.note). Opening the inbox for writing changes nothing in it.
//
// When the server cannot be asked, the error says why, and nothing is
// remembered, so the next call asks again. Two calls at the same time may both
// ask. That is harmless: the answer is the same.
func (b *Backend) tagsSupported(ctx context.Context) (bool, error) {
	if supported, known := b.tags.get(); known {
		return supported, nil
	}
	err := b.pool.with(ctx, func(c *imapclient.Client) error {
		_, err := b.openFolder(c, inboxName, false)
		return err
	})
	if err != nil {
		return false, err
	}
	supported, _ := b.tags.get()
	return supported, nil
}

var errNoTags = fmt.Errorf("%w: this account does not let mail clients create new tags", mail.ErrUnsupported)
