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

// openMailbox opens a folder. With readOnly, it uses EXAMINE: a read-only
// folder cannot change, so reading never sets \Seen or \Recent. Without it,
// it uses SELECT, for the commands that change messages. The data holds the
// UIDVALIDITY, the UIDNEXT, the flags that the folder uses and the permanent
// flags.
//
// A folder that does not exist gives an error that wraps mail.ErrNotFound.
//
// Code in a Backend method uses Backend.openFolder, which also notes whether
// the account supports tags.
func openMailbox(c *imapclient.Client, mailbox string, readOnly bool) (*imap.SelectData, error) {
	data, err := c.Select(mailbox, &imap.SelectOptions{ReadOnly: readOnly}).Wait()
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

// CreateFolder creates a folder and returns it. The parent may be empty (a
// top-level folder), a folder alias or a folder name. The name of the new
// folder is the name of the parent, the hierarchy delimiter of the server and
// the name.
//
// The name may not be empty, may not have a control character, the delimiter
// of the server, or one of the LIST wildcards (* and %), and may not be a
// folder alias. A name that is not valid wraps mail.ErrUnsupported. A parent
// that does not exist wraps mail.ErrNotFound, and the backend does not make it.
// A folder that exists already wraps mail.ErrConflict.
//
// CREATE is not repeated after a dropped connection: the folder may exist
// already, and the second try would report a conflict that is not real.
func (b *Backend) CreateFolder(ctx context.Context, name, parent string) (mail.Folder, error) {
	if err := checkNewFolderName(name); err != nil {
		return mail.Folder{}, err
	}
	if err := checkFolderName(parent); err != nil {
		return mail.Folder{}, err
	}
	if strings.ContainsAny(parent, "*%") {
		return mail.Folder{}, fmt.Errorf("%w: the folder name is not valid", mail.ErrNotFound)
	}

	var out mail.Folder
	err := b.pool.withNoRetry(ctx, func(c *imapclient.Client) error {
		out = mail.Folder{}
		delim, err := hierarchyDelimiter(c)
		if err != nil {
			return err
		}
		if delim != 0 && strings.ContainsRune(name, delim) {
			return fmt.Errorf("%w: a folder name may not contain %q. Use the parent to make a folder inside a folder", mail.ErrUnsupported, string(delim))
		}

		full := name
		if parent != "" {
			parentName, err := resolveOn(c, parent)
			if err != nil {
				return err
			}
			// A parent that only holds other folders (\Noselect) is fine.
			parentData, err := listExact(c, parentName)
			if err != nil {
				return err
			}
			if parentData == nil {
				return fmt.Errorf("%w: the parent folder %q does not exist", mail.ErrNotFound, parentName)
			}
			if delim == 0 {
				return fmt.Errorf("%w: this server has no folder hierarchy, so a folder cannot have a parent", mail.ErrUnsupported)
			}
			full = parentName + string(delim) + name
		}
		if len(full) > maxFolderNameLen {
			return fmt.Errorf("%w: the folder name is too long", mail.ErrUnsupported)
		}

		existing, err := listExact(c, full)
		if err != nil {
			return err
		}
		if existing != nil {
			return fmt.Errorf("%w: the folder %q exists", mail.ErrConflict, full)
		}
		if err := c.Create(full, nil).Wait(); err != nil {
			var serverErr *imap.Error
			if errors.As(err, &serverErr) && serverErr.Code == imap.ResponseCodeAlreadyExists {
				return fmt.Errorf("%w: the folder %q exists", mail.ErrConflict, full)
			}
			return err
		}

		out = mail.Folder{Name: full}
		// The alias is a help to the caller. The folder exists now, so a
		// failed LIST does not make the call fail: an error here would make
		// the caller try again and get a conflict.
		if list, err := listMailboxes(c, false); err == nil {
			aliases := aliasFolders(list)
			for _, a := range aliasAttrs {
				if aliases[a.alias] == full {
					out.Alias = a.alias
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		return mail.Folder{}, err
	}
	return out, nil
}

// checkNewFolderName checks the name of a folder that is going to be made. The
// error wraps mail.ErrUnsupported. The hierarchy delimiter is checked later,
// because only the server knows it.
func checkNewFolderName(name string) error {
	switch {
	case strings.TrimSpace(name) == "":
		return fmt.Errorf("%w: the folder name is empty", mail.ErrUnsupported)
	case len(name) > maxFolderNameLen || strings.IndexFunc(name, unicode.IsControl) >= 0:
		return fmt.Errorf("%w: a folder name may not be longer than %d bytes or have control characters", mail.ErrUnsupported, maxFolderNameLen)
	case strings.ContainsAny(name, "*%"):
		return fmt.Errorf("%w: a folder name may not contain * or %%", mail.ErrUnsupported)
	case mail.IsAlias(name):
		return fmt.Errorf("%w: %q is a folder alias and cannot be the name of a folder", mail.ErrUnsupported, name)
	}
	return nil
}

// hierarchyDelimiter returns the hierarchy delimiter of the server: the
// character between a folder and its sub-folder. It is 0 for a server with
// flat folders. LIST with an empty name asks for it (RFC 3501, section 6.3.8).
func hierarchyDelimiter(c *imapclient.Client) (rune, error) {
	list, err := c.List("", "", nil).Collect()
	if err != nil {
		return 0, err
	}
	if len(list) == 0 {
		return 0, nil
	}
	return list[0].Delim, nil
}

// listExact returns the folder with exactly this name, or nil when there is
// none. The name must not have the LIST wildcards. A folder that is only a
// name that was subscribed to (\NonExistent) does not exist.
func listExact(c *imapclient.Client, name string) (*imap.ListData, error) {
	list, err := c.List("", name, nil).Collect()
	if err != nil {
		return nil, err
	}
	for _, d := range list {
		same := d.Mailbox == name || (strings.EqualFold(name, inboxName) && strings.EqualFold(d.Mailbox, inboxName))
		if same && !hasAttr(d.Attrs, imap.MailboxAttrNonExistent) {
			return d, nil
		}
	}
	return nil, nil
}
