package mime

import (
	"fmt"
	netmail "net/mail"
	"strings"
	"unicode"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
)

// recipients are the people a message goes to, as parsed addresses.
type recipients struct {
	to, cc, bcc []*netmail.Address
}

// envelope returns the plain addresses of all recipients for the SMTP
// envelope: To, then Cc, then Bcc.
func (r recipients) envelope() []string {
	out := make([]string, 0, len(r.to)+len(r.cc)+len(r.bcc))
	for _, list := range [][]*netmail.Address{r.to, r.cc, r.bcc} {
		for _, a := range list {
			out = append(out, a.Address)
		}
	}
	return out
}

func (r recipients) empty() bool {
	return len(r.to)+len(r.cc)+len(r.bcc) == 0
}

// dedupe removes repeated addresses. Case does not matter. The first one is
// kept, in the order To, Cc, Bcc, so an address that is in To and in Bcc stays
// in To only.
func (r *recipients) dedupe() {
	seen := map[string]bool{}
	keep := func(list []*netmail.Address) []*netmail.Address {
		var out []*netmail.Address
		for _, a := range list {
			if key := addressKey(a.Address); !seen[key] {
				seen[key] = true
				out = append(out, a)
			}
		}
		return out
	}
	r.to, r.cc, r.bcc = keep(r.to), keep(r.cc), keep(r.bcc)
}

// without returns list without the addresses in drop.
func without(list []*netmail.Address, drop map[string]bool) []*netmail.Address {
	var out []*netmail.Address
	for _, a := range list {
		if !drop[addressKey(a.Address)] {
			out = append(out, a)
		}
	}
	return out
}

// addressKey is the form of an address that is compared. Addresses are
// compared without regard to case.
func addressKey(address string) string {
	return strings.ToLower(address)
}

// resolveRecipients finds the recipients of a message: the people a reply
// answers, then the addresses in Compose. Each address is once in the result.
func resolveRecipients(c mail.Compose, bc BuildContext) (recipients, error) {
	var r recipients
	if c.ReplyTo != nil {
		var err error
		if r.to, r.cc, err = replyRecipients(bc.Original, c.ReplyTo.All); err != nil {
			return recipients{}, err
		}
	}
	for _, part := range []struct {
		field string
		given []string
		into  *[]*netmail.Address
	}{{"to", c.To, &r.to}, {"cc", c.Cc, &r.cc}, {"bcc", c.Bcc, &r.bcc}} {
		list, err := parseRecipients(part.field, part.given)
		if err != nil {
			return recipients{}, err
		}
		*part.into = append(*part.into, list...)
	}

	if c.ReplyTo != nil && c.ReplyTo.All {
		self := map[string]bool{}
		for _, s := range bc.Self {
			self[addressKey(strings.TrimSpace(s))] = true
		}
		r.to, r.cc = without(r.to, self), without(r.cc, self)
	}
	r.dedupe()
	if r.empty() && !bc.ForDraft {
		return recipients{}, ErrNoRecipients
	}
	return r, nil
}

// replyRecipients returns the To and Cc addresses that a reply to the original
// starts with. A plain reply goes to the Reply-To addresses of the original,
// or else to its sender. A reply to all also puts the To and Cc addresses of
// the original in Cc. The display names come from a third party, so they are
// cleaned (see cleanOriginal).
func replyRecipients(original *mail.Message, all bool) (to, cc []*netmail.Address, err error) {
	direct, field := original.ReplyTo, "original reply-to"
	if len(direct) == 0 {
		direct, field = []mail.Address{original.From}, "original from"
	}
	if to, err = originalAddresses(field, direct); err != nil || !all {
		return to, nil, err
	}
	copied, err := originalAddresses("original to", original.To)
	if err != nil {
		return nil, nil, err
	}
	more, err := originalAddresses("original cc", original.Cc)
	if err != nil {
		return nil, nil, err
	}
	return to, append(copied, more...), nil
}

// originalAddresses is plainAddresses for addresses of the original message.
// It cleans the display names first.
func originalAddresses(field string, list []mail.Address) ([]*netmail.Address, error) {
	cleaned := make([]mail.Address, len(list))
	for i, a := range list {
		cleaned[i] = mail.Address{Name: cleanOriginal(a.Name), Email: a.Email}
	}
	return plainAddresses(field, cleaned)
}

// parseRecipients parses the address strings of one Compose field. Each
// string must hold one address.
func parseRecipients(field string, given []string) ([]*netmail.Address, error) {
	out := make([]*netmail.Address, 0, len(given))
	for i, s := range given {
		name := fmt.Sprintf("%s[%d]", field, i)
		if err := checkValue(name, s); err != nil {
			return nil, err
		}
		a, err := netmail.ParseAddress(s)
		if err != nil {
			// The error of the parser holds the text of the address.
			return nil, fmt.Errorf("%w: %s", ErrInvalidAddress, name)
		}
		if err := checkAddress(name, a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// checkAddress checks a parsed address. The name and the address must not
// have a CR, an LF or a NUL (ErrHeaderInjection), and the address must be safe
// to use in the SMTP envelope (ErrInvalidAddress, see isEnvelopeSafe).
func checkAddress(field string, a *netmail.Address) error {
	if err := checkValue(field, a.Name); err != nil {
		return err
	}
	if err := checkValue(field, a.Address); err != nil {
		return err
	}
	if !isEnvelopeSafe(a.Address) {
		return fmt.Errorf("%w: %s", ErrInvalidAddress, field)
	}
	return nil
}

// isEnvelopeSafe reports whether an address can be written into an SMTP path,
// "RCPT TO:<address>", exactly as it is.
//
// net/mail removes the quotes of a quoted local part and returns the text
// that was inside. For "a> NOTIFY=SUCCESS"@x it returns a> NOTIFY=SUCCESS@x.
// In the envelope the ">" would end the path early, and the rest would be read
// as SMTP parameters, while the header still shows the quoted address. So an
// address is refused when it has a space or one of these characters: a double
// quote, < > ( ) [ ] , ; : and a backslash. The square brackets make a domain
// literal such as bob@[1.2.3.4] unsafe too. An address is also refused when
// net/mail would have to quote it again (a dot at the start or the end of the
// local part, two dots in a row, an "@" in the local part, and so on).
//
// Any other white space or control character is refused too: the line and
// paragraph separators (U+2028, U+2029), U+0085, the no-break space and the
// other wide spaces. The SMTP code refuses them in the envelope, so a message
// with one could never be sent, and a confirmation would show the character as
// a plain space.
func isEnvelopeSafe(address string) bool {
	if address == "" || strings.ContainsAny(address, " \"<>()[],;:\\") {
		return false
	}
	if strings.ContainsFunc(address, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return false
	}
	return !strings.Contains((&netmail.Address{Address: address}).String(), `"`)
}

// plainAddresses is plainAddress for a list.
func plainAddresses(field string, list []mail.Address) ([]*netmail.Address, error) {
	out := make([]*netmail.Address, 0, len(list))
	for i, a := range list {
		p, err := plainAddress(fmt.Sprintf("%s[%d]", field, i), a)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// plainAddress checks a mail.Address and returns it as a net/mail address.
// The Email field must be one bare address, such as "bob@example.com", and
// not "Bob <bob@example.com>". It must be safe for the SMTP envelope too (see
// isEnvelopeSafe).
func plainAddress(field string, a mail.Address) (*netmail.Address, error) {
	if err := checkValue(field, a.Name); err != nil {
		return nil, err
	}
	if err := checkValue(field, a.Email); err != nil {
		return nil, err
	}
	p, err := netmail.ParseAddress(a.Email)
	if err != nil || p.Name != "" || p.Address != a.Email || !isEnvelopeSafe(p.Address) {
		return nil, fmt.Errorf("%w: %s", ErrInvalidAddress, field)
	}
	return &netmail.Address{Name: a.Name, Address: a.Email}, nil
}
