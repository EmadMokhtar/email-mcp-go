package imapsmtp

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
)

// This file has the calls that change the flags and tags of messages, and the
// batch that all calls that change messages share (Move and Trash are in
// move.go, CreateFolder is in folders.go). They are the first calls of the
// backend that change what an agent works on, so they follow a few rules:
//
//   - A call that takes many refs checks all of them before it changes
//     anything (see batch). A ref that is stale or missing fails the whole
//     call, and nothing is changed.
//   - Nothing is deleted for good. Trash moves a message to the Trash folder.
//     The only EXPUNGE is "UID EXPUNGE" of the messages that the fallback of
//     Move has just copied (see moveGroup).
//   - A tag is never a system flag (see isTag).

// refGroup is the part of a batch that lies in one folder with one
// UIDVALIDITY.
type refGroup struct {
	mailbox     string
	uidValidity uint32
	// uids has every UID of the group once, lowest first.
	uids []imap.UID

	// noop is set by Move: the folder is the target, so the messages stay.
	noop bool
	// newRefs is set by Move: the new ref of each UID that moved.
	newRefs map[imap.UID]mail.Ref
}

// ref returns the ref data that names the folder of the group.
func (g *refGroup) ref() imapRef { return imapRef{Mailbox: g.mailbox, UIDValidity: g.uidValidity} }

// uidSet returns the UIDs of the group as a set for a command.
func (g *refGroup) uidSet() imap.UIDSet { return imap.UIDSetNum(g.uids...) }

// batchItem is one ref of the call: the ref as the caller gave it, the UID,
// and the group that the UID is in.
type batchItem struct {
	ref   mail.Ref
	uid   imap.UID
	group *refGroup
}

// batch is the refs of one call, read and grouped by folder. A batch can name
// the same message twice. The messages are changed once.
type batch struct {
	// groups are in the order in which their folders first appear in the call.
	groups []*refGroup
	// items has one entry for each ref of the call, in the same order.
	items []batchItem
}

// newBatch reads the refs. It makes no use of the server. A call of more than
// maxBatch refs, and a ref that this backend could not have made, are errors.
func newBatch(refs []mail.Ref) (*batch, error) {
	if len(refs) > maxBatch {
		return nil, fmt.Errorf("%w: at most %d messages can be changed in one call, got %d", mail.ErrUnsupported, maxBatch, len(refs))
	}
	type key struct {
		mailbox     string
		uidValidity uint32
	}
	index := make(map[key]*refGroup)
	bt := &batch{items: make([]batchItem, len(refs))}
	for i, ref := range refs {
		r, err := openRef(ref)
		if err != nil {
			return nil, fmt.Errorf("reference %d: %w", i+1, err)
		}
		if strings.EqualFold(r.Mailbox, inboxName) {
			r.Mailbox = inboxName
		}
		k := key{r.Mailbox, r.UIDValidity}
		g := index[k]
		if g == nil {
			g = &refGroup{mailbox: r.Mailbox, uidValidity: r.UIDValidity}
			index[k] = g
			bt.groups = append(bt.groups, g)
		}
		g.uids = append(g.uids, imap.UID(r.UID))
		bt.items[i] = batchItem{ref: ref, uid: imap.UID(r.UID), group: g}
	}
	for _, g := range bt.groups {
		slices.Sort(g.uids)
		g.uids = slices.Compact(g.uids)
	}
	return bt, nil
}

// count returns the number of messages in the groups that pass keep.
func (bt *batch) count(keep func(*refGroup) bool) int {
	n := 0
	for _, g := range bt.groups {
		if keep(g) {
			n += len(g.uids)
		}
	}
	return n
}

// check opens each folder of the batch read-only and checks, for each, that
// the UIDVALIDITY is the one in the refs and that every UID is there. It
// changes nothing, so it may run again after a dropped connection.
//
// A message that is marked \Deleted counts as gone: a search does not show it,
// and an expunge by another client can remove it at any time.
func (b *Backend) check(c *imapclient.Client, bt *batch) error {
	for _, g := range bt.groups {
		if _, err := b.checkRefFolder(c, g.ref(), true); err != nil {
			return err
		}
		if err := uidsExist(c, g.uids); err != nil {
			return err
		}
	}
	return nil
}

// uidsExist checks that all the UIDs are in the selected folder and not marked
// \Deleted. The error wraps mail.ErrNotFound.
func uidsExist(c *imapclient.Client, uids []imap.UID) error {
	data, err := c.UIDSearch(&imap.SearchCriteria{
		UID:     []imap.UIDSet{imap.UIDSetNum(uids...)},
		NotFlag: []imap.Flag{imap.FlagDeleted},
	}, nil).Wait()
	if err != nil {
		return err
	}
	found := make(map[imap.UID]struct{}, len(uids))
	for _, uid := range data.AllUIDs() {
		found[uid] = struct{}{}
	}
	for _, uid := range uids {
		if _, ok := found[uid]; !ok {
			return errGone()
		}
	}
	return nil
}

// changed adds to an error what a person needs to know when a call failed
// after it had changed messages in earlier folders: the batch is not all or
// nothing from there on. Before that point it is (see check).
func changed(err error, done, total int, verb string) error {
	if done == 0 || err == nil {
		return err
	}
	return fmt.Errorf("imap: %d of %d messages were already %s when this error happened: %w", done, total, verb, err)
}

// ---------------------------------------------------------------- flags and tags

// SetFlags sets or clears \Seen and \Flagged on the messages. A nil field of
// change leaves that flag as it is.
//
// All refs are checked first (see batch). The change is then one STORE for
// each folder and each direction, in a folder opened for writing. Setting a
// flag again is harmless, so a dropped connection makes the pool run the change
// again.
func (b *Backend) SetFlags(ctx context.Context, refs []mail.Ref, change mail.FlagChange) error {
	var add, remove []imap.Flag
	for _, f := range []struct {
		value *bool
		flag  imap.Flag
	}{{change.Seen, imap.FlagSeen}, {change.Flagged, imap.FlagFlagged}} {
		switch {
		case f.value == nil:
		case *f.value:
			add = append(add, f.flag)
		default:
			remove = append(remove, f.flag)
		}
	}
	bt, err := newBatch(refs)
	if err != nil {
		return err
	}
	if len(add)+len(remove) == 0 || len(bt.groups) == 0 {
		return nil
	}
	return b.storeFlags(ctx, bt, remove, add)
}

// Tag adds and removes tags on the messages.
//
// Every name must be a tag (see isTag). Any other name, a system flag like
// \Deleted included, is refused before the server is used. When a name is in
// both lists, it is removed first and then added, so it ends up on the
// messages. The call is ErrUnsupported when the account does not support tags
// (see Capabilities). All refs are checked before anything is changed.
func (b *Backend) Tag(ctx context.Context, refs []mail.Ref, add, remove []string) error {
	if err := checkTagNames(add); err != nil {
		return err
	}
	if err := checkTagNames(remove); err != nil {
		return err
	}
	bt, err := newBatch(refs)
	if err != nil {
		return err
	}
	supported, err := b.tagsSupported(ctx)
	if err != nil {
		return err
	}
	if !supported {
		return errNoTags
	}
	if len(add)+len(remove) == 0 || len(bt.groups) == 0 {
		return nil
	}
	return b.storeFlags(ctx, bt, tagFlags(remove), tagFlags(add))
}

// storeFlags checks the refs of the batch, then removes and adds the flags in
// each folder with "UID STORE -FLAGS.SILENT" and "UID STORE +FLAGS.SILENT".
// Only these two commands change the flags, so no other flag is touched.
func (b *Backend) storeFlags(ctx context.Context, bt *batch, remove, add []imap.Flag) error {
	err := b.pool.with(ctx, func(c *imapclient.Client) error { return b.check(c, bt) })
	if err != nil {
		return err
	}
	total := bt.count(func(*refGroup) bool { return true })
	return b.pool.with(ctx, func(c *imapclient.Client) error {
		done := 0
		for _, g := range bt.groups {
			if err := b.storeGroup(c, g, remove, add); err != nil {
				return changed(err, done, total, "changed")
			}
			done += len(g.uids)
		}
		return nil
	})
}

// storeGroup opens the folder of the group for writing and changes the flags
// of its messages. It checks the UIDVALIDITY again, because the folder can
// change between the check of the batch and now.
func (b *Backend) storeGroup(c *imapclient.Client, g *refGroup, remove, add []imap.Flag) error {
	if _, err := b.checkRefFolder(c, g.ref(), false); err != nil {
		return err
	}
	set := g.uidSet()
	for _, op := range []struct {
		op    imap.StoreFlagsOp
		flags []imap.Flag
	}{{imap.StoreFlagsDel, remove}, {imap.StoreFlagsAdd, add}} {
		if len(op.flags) == 0 {
			continue
		}
		// Close reads the answer and ends the command (see pool.with).
		cmd := c.Store(set, &imap.StoreFlags{Op: op.op, Silent: true, Flags: op.flags}, nil)
		if err := cmd.Close(); err != nil {
			return err
		}
	}
	return nil
}

// CreateTag checks the name of a tag and returns it. IMAP keywords exist from
// the time they are first used, so there is nothing to create, and the server
// is not used.
func (b *Backend) CreateTag(_ context.Context, name string) (mail.Tag, error) {
	if err := checkTagName(name); err != nil {
		return mail.Tag{}, err
	}
	return mail.Tag{Name: name}, nil
}

// ListTags returns the tags of the account: the keywords that the messages of
// the inbox use and that are tags (see isTag), sorted. It is ErrUnsupported
// when the account does not support tags.
func (b *Backend) ListTags(ctx context.Context) ([]mail.Tag, error) {
	if supported, known := b.tags.get(); known && !supported {
		return nil, errNoTags
	}
	var flags []imap.Flag
	err := b.pool.with(ctx, func(c *imapclient.Client) error {
		// While it is not known whether the account supports tags, the inbox
		// is opened for writing, because that answer is final (see
		// tagSupport.note). Later, a read-only open is enough.
		_, known := b.tags.get()
		sel, err := b.openFolder(c, inboxName, known)
		if err != nil {
			return err
		}
		flags = sel.Flags
		return nil
	})
	if err != nil {
		return nil, err
	}
	if supported, _ := b.tags.get(); !supported {
		return nil, errNoTags
	}
	names := make([]string, 0, len(flags))
	for _, f := range flags {
		if isTag(string(f)) {
			names = append(names, string(f))
		}
	}
	slices.Sort(names)
	names = slices.Compact(names)
	tags := make([]mail.Tag, len(names))
	for i, name := range names {
		tags[i] = mail.Tag{Name: name}
	}
	return tags, nil
}
