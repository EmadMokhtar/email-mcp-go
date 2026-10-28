package imapsmtp

import (
	"context"
	"errors"
	"fmt"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
)

// Trash moves the messages to the Trash folder (the folder with the \Trash
// attribute). It deletes nothing for good. When the account has no Trash
// folder, the error wraps mail.ErrNotFound and nothing is changed: a folder is
// never guessed by its name.
func (b *Backend) Trash(ctx context.Context, refs []mail.Ref) error {
	_, err := b.Move(ctx, refs, mail.AliasTrash)
	return err
}

// Move moves the messages to a folder (a name or an alias) and returns their
// new refs, in the order of refs. The old refs are not valid after a move.
//
// A message that is in the folder already stays, and its ref is returned as it
// was. A UID that is twice in the call moves once.
//
// All refs and the folder are checked before anything is changed (see batch),
// and when the server lacks both MOVE and UIDPLUS, nothing is changed and the
// error wraps mail.ErrUnsupported.
//
// With MOVE, one UID MOVE for each folder does it. Without MOVE, the fallback
// is, for each folder:
//
//  1. UID COPY of the messages. If the server does not report the new UIDs
//     (COPYUID) the fallback stops here. The messages are copied, but they
//     stay in the folder as they were. The backend does not delete a message
//     that it cannot find again.
//  2. UID STORE +FLAGS.SILENT (\Deleted) on the same UIDs.
//  3. UID EXPUNGE of the same UIDs. This needs UIDPLUS. A plain EXPUNGE would
//     also remove other messages that are marked \Deleted, for example by the
//     mail client of the user. It is never sent.
//
// The new refs come from the COPYUID answer of the server. The backend does not
// guess them. When the server sends no COPYUID for a MOVE (a server with MOVE
// and without UIDPLUS), the messages are moved, and the error says so.
//
// The change is made once: a dropped connection is not retried, because the
// command may have worked, and a second copy would make duplicates. An error
// after a change was made (some messages moved, copies made, or new refs not
// known) wraps mail.ErrPartial.
func (b *Backend) Move(ctx context.Context, refs []mail.Ref, folder string) ([]mail.Ref, error) {
	if err := checkFolderName(folder); err != nil {
		return nil, err
	}
	bt, err := newBatch(refs)
	if err != nil {
		return nil, err
	}
	if len(bt.groups) == 0 {
		return []mail.Ref{}, nil
	}
	target, err := b.resolveFolder(ctx, folder)
	if err != nil {
		return nil, err
	}
	moving := false
	for _, g := range bt.groups {
		g.noop = g.mailbox == target
		g.newRefs = nil
		moving = moving || !g.noop
	}

	// Checks that change nothing.
	err = b.pool.with(ctx, func(c *imapclient.Client) error {
		if err := b.check(c, bt); err != nil {
			return err
		}
		if moving {
			// The folder must exist. Without this, the first MOVE would fail,
			// which is also safe, but a clear error comes sooner.
			_, err := b.openFolder(c, target, true)
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if moving {
		err = b.pool.withNoRetry(ctx, func(c *imapclient.Client) error { return b.applyMove(c, bt, target) })
		if err != nil {
			return nil, err
		}
	}

	out := make([]mail.Ref, len(refs))
	for i, item := range bt.items {
		if item.group.noop {
			out[i] = item.ref
			continue
		}
		out[i] = item.group.newRefs[item.uid]
		if out[i] == "" {
			return nil, partial(fmt.Errorf("imap: the messages were moved, but the new reference of message %d is not known", i+1))
		}
	}
	return out, nil
}

// applyMove moves the groups that are not in the target folder already.
func (b *Backend) applyMove(c *imapclient.Client, bt *batch, target string) error {
	hasMove := c.Caps().Has(imap.CapMove)
	if !hasMove && !c.Caps().Has(imap.CapUIDPlus) {
		// Decided before the first change, so a refused call changes nothing.
		return fmt.Errorf("%w: server lacks MOVE and UIDPLUS", mail.ErrUnsupported)
	}
	total := bt.count(func(g *refGroup) bool { return !g.noop })
	done := 0
	for _, g := range bt.groups {
		if g.noop {
			continue
		}
		if err := b.moveGroup(c, g, target, hasMove); err != nil {
			return changed(err, done, total, "moved")
		}
		done += len(g.uids)
	}
	return nil
}

// moveGroup moves the messages of one group and sets g.newRefs.
func (b *Backend) moveGroup(c *imapclient.Client, g *refGroup, target string, hasMove bool) error {
	// The folder is opened for writing. The UIDVALIDITY is checked again.
	if _, err := b.checkRefFolder(c, g.ref(), false); err != nil {
		return err
	}
	set := g.uidSet()

	if hasMove {
		// Client.Move of go-imap would fall back to COPY, STORE and a plain
		// EXPUNGE when MOVE is missing. That is why the fallback is written
		// here, and why this branch needs hasMove.
		data, err := c.Move(set, target).Wait()
		if err != nil {
			return moveError(err, target)
		}
		g.newRefs, err = copiedRefs(target, g.uids, data.UIDValidity, data.SourceUIDs, data.DestUIDs)
		if err != nil {
			return partial(fmt.Errorf("the messages were moved to %q, but their new references are not known: %w", target, err))
		}
		return nil
	}

	cpy, err := c.Copy(set, target).Wait()
	if err != nil {
		return moveError(err, target)
	}
	newRefs, err := copiedRefs(target, g.uids, cpy.UIDValidity, cpy.SourceUIDs, cpy.DestUIDs)
	if err != nil {
		// Stop before the source is touched. The copy stays, and so does the
		// original.
		return partial(fmt.Errorf("the messages were copied to %q but not removed from the folder, because their new references are not known: %w", target, err))
	}
	// Mark only the messages that were copied, and expunge only them.
	del := c.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil)
	if err := del.Close(); err != nil {
		return partial(fmt.Errorf("the messages were copied to %q but could not be marked as deleted in the folder: %w", target, err))
	}
	if err := c.UIDExpunge(set).Close(); err != nil {
		return partial(fmt.Errorf("the messages were copied to %q and marked as deleted in the folder, but the server did not remove them: %w", target, err))
	}
	g.newRefs = newRefs
	return nil
}

// moveError turns the refusal of the server for a missing target folder into
// an error that wraps mail.ErrNotFound. Other errors are returned as they are.
func moveError(err error, target string) error {
	var serverErr *imap.Error
	if errors.As(err, &serverErr) &&
		(serverErr.Code == imap.ResponseCodeTryCreate || serverErr.Code == imap.ResponseCodeNonExistent) {
		return fmt.Errorf("%w: folder %q", mail.ErrNotFound, target)
	}
	return err
}

// copiedRefs makes the new ref of each message from the COPYUID answer.
func copiedRefs(target string, want []imap.UID, uidValidity uint32, src, dst imap.NumSet) (map[imap.UID]mail.Ref, error) {
	if uidValidity == 0 {
		return nil, fmt.Errorf("%w: the server did not report the new UIDs (COPYUID) of the messages", mail.ErrUnsupported)
	}
	uids, err := newUIDs(want, src, dst)
	if err != nil {
		return nil, err
	}
	refs := make(map[imap.UID]mail.Ref, len(uids))
	for old, uid := range uids {
		refs[old] = encodeRef(imapRef{Mailbox: target, UIDValidity: uidValidity, UID: uint32(uid)})
	}
	return refs, nil
}

// newUIDs reads the UID sets of a COPYUID answer and returns the new UID of
// each UID in want. The first UID of src is the old UID of the first UID of
// dst, and so on (RFC 4315). A set is read in ascending order, which is how
// go-imap keeps it and how servers write it.
//
// It is an error, wrapping mail.ErrUnsupported, when the server sent no sets,
// when the sets are not as long as each other, or when a UID of want is not
// in src. The backend does not guess a UID. A set is measured before it is
// read out, so a server cannot make the backend build a huge list.
func newUIDs(want []imap.UID, src, dst imap.NumSet) (map[imap.UID]imap.UID, error) {
	noCopyUID := fmt.Errorf("%w: the server did not report the new UIDs (COPYUID) of the messages", mail.ErrUnsupported)
	srcSet, ok1 := src.(imap.UIDSet)
	dstSet, ok2 := dst.(imap.UIDSet)
	if !ok1 || !ok2 || len(srcSet) == 0 || len(dstSet) == 0 {
		return nil, noCopyUID
	}
	n := uidSetSize(srcSet)
	if n != uidSetSize(dstSet) || n > uint64(maxBatch) {
		return nil, fmt.Errorf("%w: the server reported UID sets for the move (COPYUID) that do not fit the messages", mail.ErrUnsupported)
	}
	from, ok1 := srcSet.Nums()
	to, ok2 := dstSet.Nums()
	if !ok1 || !ok2 || len(from) != len(to) {
		return nil, noCopyUID
	}
	byOld := make(map[imap.UID]imap.UID, len(from))
	for i, uid := range from {
		byOld[uid] = to[i]
	}
	out := make(map[imap.UID]imap.UID, len(want))
	for _, uid := range want {
		nu, ok := byOld[uid]
		if !ok {
			return nil, fmt.Errorf("%w: the server did not report the new UID (COPYUID) of every message", mail.ErrUnsupported)
		}
		out[uid] = nu
	}
	return out, nil
}

// uidSetSize returns how many UIDs a set holds, without reading them out.
func uidSetSize(s imap.UIDSet) uint64 {
	var n uint64
	for _, r := range s {
		if r.Stop < r.Start {
			continue // not possible for a set that go-imap read
		}
		n += uint64(r.Stop-r.Start) + 1
	}
	return n
}
