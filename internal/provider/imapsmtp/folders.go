package imapsmtp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
)

// inboxName is the name of the inbox. IMAP defines it, and it is not case
// sensitive.
const inboxName = "INBOX"

// aliasAttrs links each folder alias to the SPECIAL-USE attribute (RFC 6154)
// that a server puts on that folder. The inbox has no attribute, it has a
// fixed name.
var aliasAttrs = []struct {
	alias string
	attr  imap.MailboxAttr
}{
	{mail.AliasArchive, imap.MailboxAttrArchive},
	{mail.AliasSent, imap.MailboxAttrSent},
	{mail.AliasDrafts, imap.MailboxAttrDrafts},
	{mail.AliasTrash, imap.MailboxAttrTrash},
	{mail.AliasJunk, imap.MailboxAttrJunk},
}

// ListFolders returns the folders of the account that can hold messages. The
// folder alias comes from the SPECIAL-USE attribute of the folder.
//
// The counts (Unread and Total) are set when the server supports LIST-STATUS,
// because then the server sends them with the list in one command. Without
// LIST-STATUS they are nil: the backend does not send one STATUS command for
// each folder.
func (b *Backend) ListFolders(ctx context.Context) ([]mail.Folder, error) {
	var folders []mail.Folder
	err := b.pool.with(ctx, func(c *imapclient.Client) error {
		list, err := listMailboxes(c, true)
		if err != nil {
			return err
		}
		aliasOf := make(map[string]string, len(list))
		for alias, name := range aliasFolders(list) {
			aliasOf[name] = alias
		}
		folders = make([]mail.Folder, 0, len(list))
		for _, data := range list {
			f := mail.Folder{Name: data.Mailbox, Alias: aliasOf[data.Mailbox]}
			if st := data.Status; st != nil {
				if st.NumMessages != nil {
					total := int(*st.NumMessages)
					f.Total = &total
				}
				if st.NumUnseen != nil {
					unread := int(*st.NumUnseen)
					f.Unread = &unread
				}
			}
			folders = append(folders, f)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return folders, nil
}

// resolveFolder returns the name of the folder that nameOrAlias means. A
// folder alias is looked up through the SPECIAL-USE attributes of the folders.
// "@inbox" and the name INBOX (in any case) give INBOX. An empty name also
// means the inbox. Any other name is returned as it is, and the server tells
// later whether the folder exists.
//
// When no folder has the attribute of the alias, the error wraps
// mail.ErrNotFound.
func (b *Backend) resolveFolder(ctx context.Context, nameOrAlias string) (string, error) {
	if err := checkFolderName(nameOrAlias); err != nil {
		return "", err
	}
	if name, ok := plainFolder(nameOrAlias); ok {
		return name, nil
	}
	var name string
	err := b.pool.with(ctx, func(c *imapclient.Client) error {
		var err error
		name, err = resolveOn(c, nameOrAlias)
		return err
	})
	return name, err
}

// maxFolderNameLen is the longest folder name that the backend sends to a
// server. Servers limit names to about 1000 bytes.
const maxFolderNameLen = 1024

// plainFolder returns the folder name for a name that needs no question to the
// server: the inbox and every name that is not an alias.
func plainFolder(nameOrAlias string) (string, bool) {
	switch {
	case nameOrAlias == "" || nameOrAlias == mail.AliasInbox || strings.EqualFold(nameOrAlias, inboxName):
		return inboxName, true
	case mail.IsAlias(nameOrAlias):
		return "", false
	default:
		return nameOrAlias, true
	}
}

// checkFolderName rejects a name that no IMAP folder can have: one that is too
// long or has a control character (NUL, CR, LF and the like). The error wraps
// mail.ErrNotFound. The name comes from an agent, and the server should not
// have to deal with it.
func checkFolderName(name string) error {
	if len(name) > maxFolderNameLen || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return fmt.Errorf("%w: the folder name is not valid", mail.ErrNotFound)
	}
	return nil
}

// resolveOn is resolveFolder for code that already has a connection.
func resolveOn(c *imapclient.Client, nameOrAlias string) (string, error) {
	if err := checkFolderName(nameOrAlias); err != nil {
		return "", err
	}
	if name, ok := plainFolder(nameOrAlias); ok {
		return name, nil
	}
	list, err := listMailboxes(c, false)
	if err != nil {
		return "", err
	}
	if name, ok := aliasFolders(list)[nameOrAlias]; ok {
		return name, nil
	}
	return "", fmt.Errorf("%w: no %s folder", mail.ErrNotFound, nameOrAlias)
}

// aliasFolders returns the folder name of each alias, from the SPECIAL-USE
// attributes in a list. When two folders have the same attribute, the first
// one wins. The inbox is always there.
func aliasFolders(list []*imap.ListData) map[string]string {
	out := map[string]string{mail.AliasInbox: inboxName}
	for _, data := range list {
		for _, a := range aliasAttrs {
			if _, taken := out[a.alias]; !taken && hasAttr(data.Attrs, a.attr) {
				out[a.alias] = data.Mailbox
			}
		}
	}
	return out
}

// hasAttr reports whether attrs has attr. Attributes are not case sensitive.
func hasAttr(attrs []imap.MailboxAttr, attr imap.MailboxAttr) bool {
	return slices.ContainsFunc(attrs, func(a imap.MailboxAttr) bool {
		return strings.EqualFold(string(a), string(attr))
	})
}

// listMailboxes sends LIST for all folders. It asks for the SPECIAL-USE
// attributes when the server supports that, and for the message counts when
// withStatus is true and the server supports LIST-STATUS. Folders that cannot
// hold messages (\Noselect and \NonExistent) are left out.
func listMailboxes(c *imapclient.Client, withStatus bool) ([]*imap.ListData, error) {
	caps := c.Caps()
	var opts *imap.ListOptions
	// The return options of LIST need LIST-EXTENDED. A server that does not
	// know a return option answers BAD, so each option is sent only when the
	// server announced it. IMAP4rev2 includes SPECIAL-USE and LIST-STATUS.
	if caps.Has(imap.CapListExtended) {
		opts = &imap.ListOptions{
			ReturnSpecialUse: caps.Has(imap.CapSpecialUse) || caps.Has(imap.CapIMAP4rev2),
		}
		if withStatus && caps.Has(imap.CapListStatus) {
			opts.ReturnStatus = &imap.StatusOptions{NumMessages: true, NumUnseen: true}
		}
	}
	all, err := c.List("", "*", opts).Collect()
	if err != nil {
		return nil, err
	}
	list := all[:0]
	for _, data := range all {
		if hasAttr(data.Attrs, imap.MailboxAttrNoSelect) || hasAttr(data.Attrs, imap.MailboxAttrNonExistent) {
			continue
		}
		list = append(list, data)
	}
	return list, nil
}

// examine opens a folder read-only (EXAMINE). A read-only folder cannot
// change, so reading never sets \Seen or \Recent. The data holds the
// UIDVALIDITY, the UIDNEXT and the flags that the folder uses.
//
// A folder that does not exist gives an error that wraps mail.ErrNotFound.
func examine(c *imapclient.Client, mailbox string) (*imap.SelectData, error) {
	data, err := c.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		var serverErr *imap.Error
		if errors.As(err, &serverErr) && serverErr.Type == imap.StatusResponseTypeNo &&
			(serverErr.Code == "" || serverErr.Code == imap.ResponseCodeNonExistent) {
			return nil, fmt.Errorf("%w: folder %q", mail.ErrNotFound, mailbox)
		}
		return nil, err
	}
	if data.UIDValidity == 0 {
		// RFC 3501 requires UIDVALIDITY. Without it, a ref cannot tell
		// whether a UID still names the same message.
		return nil, fmt.Errorf("%w: the server does not report UIDVALIDITY", mail.ErrUnsupported)
	}
	return data, nil
}
