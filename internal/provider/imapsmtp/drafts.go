package imapsmtp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	netmail "net/mail"
	"strings"
	"sync/atomic"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
)

// appendMessage stores a message in a folder with APPEND, on the connection c.
// The data is sent as it is. The returned data holds the new UID when the
// server reports it (APPENDUID, from UIDPLUS). It is empty when the server does
// not.
//
// The call finishes the command before it returns, also when it fails (see the
// rule on pool.with). When the server refuses the message, the error is the
// answer of the server and the connection stays usable. It does not matter
// whether the server refuses before the message is sent (for example NO
// [TOOBIG]) or after it.
//
// APPEND must not be repeated after a dropped connection, because the server
// may have stored the message. So call this only from pool.withNoRetry.
func appendMessage(c *imapclient.Client, folder string, data []byte, flags []imap.Flag) (*imap.AppendData, error) {
	cmd := c.Append(folder, int64(len(data)), &imap.AppendOptions{Flags: flags})
	_, writeErr := cmd.Write(data)
	// Close ends the command line, and it must be called also after an error:
	// until then, the client holds the lock for sending commands.
	closeErr := cmd.Close()
	// The answer of the server is the best reason for a failure. A failed Write
	// is often only the effect of that answer.
	appended, err := cmd.Wait()
	switch {
	case err != nil:
		return nil, err
	case writeErr != nil:
		return nil, writeErr
	case closeErr != nil:
		return nil, closeErr
	}
	return appended, nil
}

// SaveDraft stores a message in the Drafts folder (the folder with the \Drafts
// attribute) with the flags \Draft and \Seen, and returns its ref.
//
// The ref comes from the APPENDUID answer of the server (UIDPLUS). Without it,
// SaveDraft searches the Drafts folder for the Message-ID of the message
// (HEADER Message-ID, with the angle brackets) and checks the Message-ID of the
// one message it finds. A message without a Message-ID cannot be found that
// way, so SaveDraft refuses it before it stores anything.
//
// Once the draft is stored, every error says "the draft was saved, but its
// reference is not known", so that nobody saves it again. This holds for the
// search that finds no message or more than one (the error then also wraps
// mail.ErrUnsupported), for a search, a fetch or a folder that fails, and for
// a time out. The wording is added in one place, after the pool returns.
//
// A missing Drafts folder gives an error that wraps mail.ErrNotFound.
//
// APPEND is not repeated after a dropped connection: the server may have stored
// the draft already, and a second one would be a duplicate.
func (b *Backend) SaveDraft(ctx context.Context, raw *mail.RawMessage) (mail.Ref, error) {
	if raw == nil || len(raw.Data) == 0 {
		return "", errors.New("imap: there is no draft to save")
	}
	wantID := headerMessageID(raw.Data)

	var ref mail.Ref
	// stored is set as soon as the server has the draft. The pool can stop
	// the call at any time (time out, ctx), and fn runs in its own goroutine, so
	// the flag is atomic.
	var stored atomic.Bool
	err := b.pool.withNoRetry(ctx, func(c *imapclient.Client) error {
		ref = ""
		folder, err := resolveOn(c, mail.AliasDrafts)
		if err != nil {
			return err
		}
		if wantID == "" && !c.Caps().Has(imap.CapUIDPlus) {
			return fmt.Errorf("%w: the draft has no Message-ID and the server does not report the new UID (UIDPLUS), so the draft could not be found again. It was not saved", mail.ErrUnsupported)
		}
		appended, err := appendMessage(c, folder, raw.Data, []imap.Flag{imap.FlagDraft, imap.FlagSeen})
		if err != nil {
			return err
		}
		stored.Store(true)
		if appended != nil && appended.UID != 0 && appended.UIDValidity != 0 {
			ref = encodeRef(imapRef{Mailbox: folder, UIDValidity: appended.UIDValidity, UID: uint32(appended.UID)})
			return nil
		}
		if wantID == "" {
			return fmt.Errorf("%w: the server did not report its UID and the draft has no Message-ID", mail.ErrUnsupported)
		}
		ref, err = b.findDraft(c, folder, wantID)
		return err
	})
	if err != nil {
		if stored.Load() {
			// The draft is in the folder. Say so, so that nobody saves it again.
			err = fmt.Errorf("imap: the draft was saved, but its reference is not known: %w", err)
		}
		return "", err
	}
	return ref, nil
}

// headerMessageID returns the Message-ID of a message in angle brackets, or an
// empty string when it has none or when it is not one word.
func headerMessageID(data []byte) string {
	msg, err := netmail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		return ""
	}
	return messageID(msg.Header.Get("Message-ID"))
}

// findDraft finds the draft that was just saved in the folder, by its
// Message-ID. The ID has its angle brackets. HEADER of IMAP matches a part of
// the header, and the brackets make a match with the whole ID. The one message
// that the search finds is checked once more, with its envelope, because a
// ref to a wrong message would later discard the wrong message.
func (b *Backend) findDraft(c *imapclient.Client, folder, id string) (mail.Ref, error) {
	sel, err := b.openFolder(c, folder, true)
	if err != nil {
		return "", err
	}
	found, err := c.UIDSearch(&imap.SearchCriteria{
		Header:  []imap.SearchCriteriaHeaderField{{Key: "Message-ID", Value: id}},
		NotFlag: []imap.Flag{imap.FlagDeleted},
	}, nil).Wait()
	if err != nil {
		return "", err
	}
	uid, err := pickDraftUID(found.AllUIDs())
	if err != nil {
		return "", err
	}
	msg, err := fetchOne(c, uid, &imap.FetchOptions{Envelope: true})
	if err != nil {
		return "", err
	}
	if msg.Envelope == nil || messageID(msg.Envelope.MessageID) != id {
		return "", fmt.Errorf("%w: the message that the search found has another Message-ID", mail.ErrUnsupported)
	}
	return encodeRef(imapRef{Mailbox: folder, UIDValidity: sel.UIDValidity, UID: uint32(uid)}), nil
}

// pickDraftUID returns the UID of the draft from the result of the search. It
// must be exactly one. The error wraps mail.ErrUnsupported.
func pickDraftUID(uids []imap.UID) (imap.UID, error) {
	if len(uids) != 1 {
		return 0, fmt.Errorf("%w: the search for the Message-ID of the draft found %d messages and not 1", mail.ErrUnsupported, len(uids))
	}
	return uids[0], nil
}

// DiscardDraft moves a draft to the Trash folder. It deletes nothing for good.
//
// The ref is checked like the refs of the other calls: a ref of a folder that
// was rebuilt gives mail.ErrRefStale, and a message that is gone gives
// mail.ErrNotFound. A message that does not have the \Draft flag is not a
// draft. The error then wraps mail.ErrUnsupported and nothing is changed. The
// flag decides and not the folder, because a mail client marks drafts with the
// flag.
//
// Between the check and the move, another client could change the message. That
// is a small risk, and the move still goes to the Trash and not out of reach.
func (b *Backend) DiscardDraft(ctx context.Context, ref mail.Ref) error {
	r, err := openRef(ref)
	if err != nil {
		return err
	}
	err = b.pool.with(ctx, func(c *imapclient.Client) error {
		if err := b.examineRef(c, r); err != nil {
			return err
		}
		msg, err := fetchOne(c, imap.UID(r.UID), &imap.FetchOptions{Flags: true})
		if err != nil {
			return err
		}
		// A message marked \Deleted counts as gone (see check).
		if hasFlag(msg.Flags, imap.FlagDeleted) {
			return errGone()
		}
		if !hasFlag(msg.Flags, imap.FlagDraft) {
			return fmt.Errorf("%w: the message is not a draft (it does not have the \\Draft flag), so it was not discarded", mail.ErrUnsupported)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return b.Trash(ctx, []mail.Ref{ref})
}

// hasFlag reports whether flags has flag. Flags are not case sensitive.
func hasFlag(flags []imap.Flag, flag imap.Flag) bool {
	for _, f := range flags {
		if strings.EqualFold(string(f), string(flag)) {
			return true
		}
	}
	return false
}
