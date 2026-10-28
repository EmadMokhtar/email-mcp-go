package imapsmtp

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/signing"
)

const (
	// defaultLimit is the page size when the query gives none.
	defaultLimit = 50
	// maxLimit is the largest page size.
	maxLimit = 100
	// maxChanges is the largest number of messages that one call of Changes
	// returns.
	maxChanges = 100
	// pageTokenTTL is how long a page token works.
	pageTokenTTL = time.Hour
)

// pageToken is the content of the token that Search returns as the next page.
// The next page holds the messages with a UID below BelowUID. The token is
// valid only while the UIDVALIDITY of the folder is the same.
type pageToken struct {
	Mailbox     string `json:"mailbox"`
	UIDValidity uint32 `json:"uidvalidity"`
	BelowUID    uint32 `json:"below_uid"`
}

// cursorToken is the content of the cursor that Changes returns. UIDNext is
// the UID where the next call starts: all messages with a lower UID were
// already returned (or were never there).
type cursorToken struct {
	Mailbox     string `json:"mailbox"`
	UIDValidity uint32 `json:"uidvalidity"`
	UIDNext     uint32 `json:"uidnext"`
}

var errNoSigner = errors.New("imap: the backend has no signer, so it cannot make page tokens and cursors")

// seal signs v for purpose, bound to the account (cfg.TokenScope). A token of
// one account is not valid for another account, even when both use the same
// signing key.
func (b *Backend) seal(purpose string, v any, ttl time.Duration) (string, error) {
	if b.signer == nil {
		return "", errNoSigner
	}
	return b.signer.Seal(purpose+":"+b.cfg.TokenScope, v, ttl)
}

// open checks a token made by seal and decodes it into v.
func (b *Backend) open(purpose, token string, v any) error {
	if b.signer == nil {
		return errNoSigner
	}
	return b.signer.Open(purpose+":"+b.cfg.TokenScope, token, v)
}

// openPageToken checks and reads a page token. A token that cannot be trusted
// gives an error that wraps signing.ErrInvalid, and an old one signing.ErrExpired.
// The caller must not search again from the first page by itself: the agent
// asked for another page.
func (b *Backend) openPageToken(token mail.PageToken) (pageToken, error) {
	var t pageToken
	if err := b.open(signing.PurposePage, string(token), &t); err != nil {
		if errors.Is(err, signing.ErrExpired) {
			return pageToken{}, fmt.Errorf("imap: the page token has expired, search again: %w", err)
		}
		return pageToken{}, fmt.Errorf("imap: the page token is not valid: %w", err)
	}
	if t.Mailbox == "" || t.BelowUID == 0 {
		return pageToken{}, fmt.Errorf("imap: the page token is not valid: %w", signing.ErrInvalid)
	}
	return t, nil
}

// openCursor checks and reads a cursor, like openPageToken.
func (b *Backend) openCursor(cursor mail.Cursor) (cursorToken, error) {
	var t cursorToken
	if err := b.open(signing.PurposeCursor, string(cursor), &t); err != nil {
		return cursorToken{}, fmt.Errorf("imap: the cursor is not valid: %w", err)
	}
	if t.Mailbox == "" || t.UIDNext == 0 {
		return cursorToken{}, fmt.Errorf("imap: the cursor is not valid: %w", signing.ErrInvalid)
	}
	return t, nil
}

// Search returns one page of the messages that match the query, newest first
// (by UID, highest first).
//
// The folder defaults to the inbox. The page size defaults to 50 and is at most
// 100. Next is set when more messages match. The page token holds the folder,
// its UIDVALIDITY and the lowest UID of this page, so the next page continues
// below that UID, also when new mail has arrived in the meantime.
//
// An old, changed or foreign page token is an error (it wraps signing.ErrExpired
// or signing.ErrInvalid). Search does not start again from the first page. A
// token for a folder that was rebuilt (it has a new UIDVALIDITY) gives
// mail.ErrRefStale. A token works only with the folder it was made for.
//
// Messages with the \Deleted flag (waiting for an expunge) are not returned.
//
// Search opens the folder read-only and reads message data with PEEK, so it
// never marks mail as read.
func (b *Backend) Search(ctx context.Context, q mail.Query, page mail.PageToken) (mail.Page[mail.Summary], error) {
	limit := q.Limit
	switch {
	case limit <= 0:
		limit = defaultLimit
	case limit > maxLimit:
		limit = maxLimit
	}
	if err := checkFolderName(q.Folder); err != nil {
		return mail.Page[mail.Summary]{}, err
	}
	var tok *pageToken
	if page != "" {
		t, err := b.openPageToken(page)
		if err != nil {
			return mail.Page[mail.Summary]{}, err
		}
		tok = &t
	}

	var out mail.Page[mail.Summary]
	// The pool may run this function twice, so it starts from nothing.
	err := b.pool.with(ctx, func(c *imapclient.Client) error {
		out = mail.Page[mail.Summary]{}
		mailbox, err := resolveOn(c, q.Folder)
		if err != nil {
			return err
		}
		if tok != nil && tok.Mailbox != mailbox {
			return fmt.Errorf("imap: the page token is for another folder: %w", signing.ErrInvalid)
		}
		sel, err := examine(c, mailbox)
		if err != nil {
			return err
		}
		var below uint32
		if tok != nil {
			if tok.UIDValidity != sel.UIDValidity {
				return fmt.Errorf("%w: the folder was rebuilt since this page token was made, search again", mail.ErrRefStale)
			}
			below = tok.BelowUID
		}

		uids, err := searchUIDs(c, q, sel.Flags, below)
		if err != nil {
			return err
		}
		selected, more, err := pickPage(c, q, uids, limit)
		if err != nil {
			return err
		}
		items, err := fetchSummaries(c, mailbox, sel.UIDValidity, selected)
		if err != nil {
			return err
		}
		out.Items = items
		if more {
			next, err := b.seal(signing.PurposePage, pageToken{
				Mailbox:     mailbox,
				UIDValidity: sel.UIDValidity,
				BelowUID:    uint32(selected[len(selected)-1]),
			}, pageTokenTTL)
			if err != nil {
				return err
			}
			out.Next = mail.PageToken(next)
		}
		return nil
	})
	if err != nil {
		return mail.Page[mail.Summary]{}, err
	}
	if out.Items == nil {
		out.Items = []mail.Summary{}
	}
	return out, nil
}

// searchUIDs runs UID SEARCH for the query in the selected folder and returns
// the UIDs of the matches, highest first. When below is not zero, only UIDs
// lower than below are searched. folderFlags are the flags the folder uses
// (the FLAGS of EXAMINE).
func searchUIDs(c *imapclient.Client, q mail.Query, folderFlags []imap.Flag, below uint32) ([]imap.UID, error) {
	criteria, impossible := buildCriteria(q, folderFlags)
	if below != 0 {
		if below == 1 {
			return nil, nil // no UID is lower than 1
		}
		criteria.UID = append(criteria.UID, imap.UIDSet{{Start: 1, Stop: imap.UID(below - 1)}})
	}
	if impossible {
		return nil, nil
	}
	data, err := c.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return nil, err
	}
	uids := data.AllUIDs()
	slices.SortFunc(uids, func(a, b imap.UID) int { return cmp.Compare(b, a) })
	return uids, nil
}

// buildCriteria turns a query into search criteria. impossible is true when
// the query can match nothing, for example when it asks for messages with a tag
// that no IMAP message can have.
//
// The date criteria are wider than the query. SINCE and BEFORE of IMAP compare
// only the date, in the time zone of the internal date of the message, and
// that zone is between UTC-12 and UTC+14. The widening makes sure the server
// returns every message that can match. pickPage then checks the exact time.
func buildCriteria(q mail.Query, folderFlags []imap.Flag) (criteria *imap.SearchCriteria, impossible bool) {
	// Messages that another client marked \Deleted wait for an expunge. Mail
	// clients do not show them, so the backend does not return them.
	c := &imap.SearchCriteria{NotFlag: []imap.Flag{imap.FlagDeleted}}
	addHeader := func(key, value string) {
		if value != "" {
			c.Header = append(c.Header, imap.SearchCriteriaHeaderField{Key: key, Value: value})
		}
	}
	addHeader("FROM", q.From)
	addHeader("TO", q.To)
	addHeader("SUBJECT", q.Subject)
	if q.Text != "" {
		c.Text = []string{q.Text}
	}

	if isSet(q.Since) {
		if day := utcDay(*q.Since).AddDate(0, 0, -1); usableDate(day) {
			c.Since = day
		}
	}
	if isSet(q.Before) {
		if day := utcDay(*q.Before).AddDate(0, 0, 2); usableDate(day) {
			c.Before = day
		}
	}

	if q.Unread != nil {
		if *q.Unread {
			c.NotFlag = append(c.NotFlag, imap.FlagSeen)
		} else {
			c.Flag = append(c.Flag, imap.FlagSeen)
		}
	}
	if q.Flagged != nil {
		if *q.Flagged {
			c.Flag = append(c.Flag, imap.FlagFlagged)
		} else {
			c.NotFlag = append(c.NotFlag, imap.FlagFlagged)
		}
	}

	// A tag is an IMAP keyword. A name that is not a valid keyword (for
	// example, one with a space) is on no message. So it never matches in
	// TagsAny, and it changes nothing in TagsNone.
	if len(q.TagsAny) > 0 {
		keywords := validKeywords(q.TagsAny)
		if len(keywords) == 0 {
			return c, true
		}
		anyOf := anyKeyword(keywords)
		c.And(&anyOf)
	}
	c.NotFlag = append(c.NotFlag, validKeywords(q.TagsNone)...)
	if q.Untagged {
		// Untagged means: none of the keywords that the folder uses.
		var used []string
		for _, f := range folderFlags {
			if !strings.HasPrefix(string(f), `\`) {
				used = append(used, string(f))
			}
		}
		c.NotFlag = append(c.NotFlag, validKeywords(used)...)
	}
	return c, false
}

// isSet reports whether a date limit of a query is used. A nil or zero time
// means no limit.
func isSet(t *time.Time) bool { return t != nil && !t.IsZero() }

// usableDate reports whether a date can be sent in a search key. The IMAP date
// has a four-digit year. A date outside this range is left out of the search.
// That is safe, because the server then returns more candidates and pickPage
// checks the exact time.
func usableDate(t time.Time) bool { return t.Year() >= 1900 && t.Year() <= 2999 }

// utcDay returns the date of t in UTC, at midnight.
func utcDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// anyKeyword returns criteria that match a message with at least one of the
// keywords. The criteria nest OR in a balanced tree, so many keywords do not
// make a deep nesting, which servers limit. keywords must not be empty.
func anyKeyword(keywords []imap.Flag) imap.SearchCriteria {
	if len(keywords) == 1 {
		return imap.SearchCriteria{Flag: []imap.Flag{keywords[0]}}
	}
	mid := len(keywords) / 2
	return imap.SearchCriteria{Or: [][2]imap.SearchCriteria{{
		anyKeyword(keywords[:mid]),
		anyKeyword(keywords[mid:]),
	}}}
}

// validKeywords returns the names that are valid IMAP keywords, as flags.
func validKeywords(names []string) []imap.Flag {
	var out []imap.Flag
	for _, n := range names {
		if isKeyword(n) {
			out = append(out, imap.Flag(n))
		}
	}
	return out
}

// isKeyword reports whether s can be an IMAP keyword (RFC 3501: an atom that
// does not start with a backslash). An atom is printable ASCII without the
// characters ( ) { % * " \ ] and white space.
func isKeyword(s string) bool {
	if s == "" || len(s) > 255 {
		return false
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch <= ' ' || ch >= 0x7f || strings.IndexByte(`(){%*"\]`, ch) >= 0 {
			return false
		}
	}
	return true
}

// hasDateFilter reports whether the query limits the date.
func hasDateFilter(q mail.Query) bool { return isSet(q.Since) || isSet(q.Before) }

// inDateRange reports whether t is in the date range of the query. Since is
// inclusive and Before is exclusive.
func inDateRange(q mail.Query, t time.Time) bool {
	if isSet(q.Since) && t.Before(*q.Since) {
		return false
	}
	if isSet(q.Before) && !t.Before(*q.Before) {
		return false
	}
	return true
}

// pickPage chooses the UIDs of one page from the UIDs the server found
// (highest first). more is true when further messages match after this page.
//
// With a date filter, the server found a few messages too many (see
// buildCriteria). pickPage reads the internal date of the candidates, newest
// first and a page at a time, until it has one page and one more message. The
// extra message tells that there is a next page.
func pickPage(c *imapclient.Client, q mail.Query, uids []imap.UID, limit int) (page []imap.UID, more bool, err error) {
	if !hasDateFilter(q) {
		if len(uids) > limit {
			return uids[:limit], true, nil
		}
		return uids, false, nil
	}
	for start := 0; start < len(uids) && len(page) <= limit; start += limit + 1 {
		batch := uids[start:min(start+limit+1, len(uids))]
		dates, err := fetchInternalDates(c, batch)
		if err != nil {
			return nil, false, err
		}
		for _, uid := range batch {
			if d, ok := dates[uid]; ok && inDateRange(q, d) {
				page = append(page, uid)
			}
		}
	}
	if len(page) > limit {
		return page[:limit], true, nil
	}
	return page, false, nil
}

// fetchInternalDates returns the internal date of each message.
func fetchInternalDates(c *imapclient.Client, uids []imap.UID) (map[imap.UID]time.Time, error) {
	msgs, err := c.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{UID: true, InternalDate: true}).Collect()
	if err != nil {
		return nil, err
	}
	dates := make(map[imap.UID]time.Time, len(msgs))
	for _, m := range msgs {
		dates[m.UID] = m.InternalDate
	}
	return dates, nil
}

// uidsInRange returns the UIDs from start up to, but not including, end, lowest
// first. The server should not send others. But a search for "UID n:*" is
// answered with the highest UID of the folder even when it is below n (RFC 3501,
// section 6.4.8), so the range is checked again here.
func uidsInRange(uids []imap.UID, start, end uint32) []imap.UID {
	var out []imap.UID
	for _, uid := range uids {
		if uint32(uid) >= start && uint32(uid) < end {
			out = append(out, uid)
		}
	}
	slices.Sort(out)
	return out
}

// Changes returns the messages that arrived in a folder since the cursor, the
// oldest first, and the cursor for the next call. At most 100 messages come
// back in one call. When there are more, the returned cursor points after the
// last message of this call, so calling Changes again with it continues.
//
// An empty cursor starts tracking: no messages come back, and the cursor points
// at the current UIDNEXT of the folder. If the UIDVALIDITY of the folder is not
// the one in the cursor (the folder was rebuilt, so the UIDs mean other
// messages), the bool result is true: no messages come back and the cursor
// starts again at the current UIDNEXT. A cursor that is changed, foreign or
// for another folder gives an error that wraps signing.ErrInvalid.
//
// Messages with the \Deleted flag (waiting for an expunge) are not returned, but
// the cursor moves past them.
//
// Changes opens the folder read-only and reads message data with PEEK, so it
// never marks mail as read.
func (b *Backend) Changes(ctx context.Context, folder string, since mail.Cursor) ([]mail.Summary, mail.Cursor, bool, error) {
	if err := checkFolderName(folder); err != nil {
		return nil, "", false, err
	}
	var cur cursorToken
	if since != "" {
		var err error
		if cur, err = b.openCursor(since); err != nil {
			return nil, "", false, err
		}
	}

	var (
		items []mail.Summary
		next  mail.Cursor
		reset bool
	)
	// The pool may run this function twice, so it starts from nothing.
	err := b.pool.with(ctx, func(c *imapclient.Client) error {
		items, next, reset = nil, "", false
		mailbox, err := resolveOn(c, folder)
		if err != nil {
			return err
		}
		if since != "" && cur.Mailbox != mailbox {
			return fmt.Errorf("imap: the cursor is for another folder: %w", signing.ErrInvalid)
		}
		sel, err := examine(c, mailbox)
		if err != nil {
			return err
		}
		if sel.UIDNext == 0 {
			return fmt.Errorf("%w: the server does not report UIDNEXT, so new messages cannot be tracked", mail.ErrUnsupported)
		}
		end := uint32(sel.UIDNext) // messages below this UID exist now
		cursorAt := func(uidNext uint32) (mail.Cursor, error) {
			tok, err := b.seal(signing.PurposeCursor, cursorToken{Mailbox: mailbox, UIDValidity: sel.UIDValidity, UIDNext: uidNext}, 0)
			return mail.Cursor(tok), err
		}

		switch {
		case since == "":
			next, err = cursorAt(end)
			return err
		case cur.UIDValidity != sel.UIDValidity || cur.UIDNext > end:
			// A cursor ahead of UIDNEXT cannot come from this folder, so it is
			// handled like a rebuilt folder.
			reset = true
			next, err = cursorAt(end)
			return err
		case cur.UIDNext == end:
			next = since // nothing new
			return nil
		}

		// UIDs from cur.UIDNext up to end-1. The range has an explicit end, so
		// the server does not add the highest UID of the folder when it is
		// below the start, as it does for "n:*".
		// Messages with the \Deleted flag are skipped. The cursor still moves
		// past them.
		criteria := &imap.SearchCriteria{
			UID:     []imap.UIDSet{{{Start: imap.UID(cur.UIDNext), Stop: imap.UID(end - 1)}}},
			NotFlag: []imap.Flag{imap.FlagDeleted},
		}
		data, err := c.UIDSearch(criteria, nil).Wait()
		if err != nil {
			return err
		}
		uids := uidsInRange(data.AllUIDs(), cur.UIDNext, end)

		resume := end
		if len(uids) > maxChanges {
			uids = uids[:maxChanges]
			resume = uint32(uids[len(uids)-1]) + 1
		}
		if items, err = fetchSummaries(c, mailbox, sel.UIDValidity, uids); err != nil {
			return err
		}
		next, err = cursorAt(resume)
		return err
	})
	if err != nil {
		return nil, "", false, err
	}
	if items == nil {
		items = []mail.Summary{}
	}
	return items, next, reset, nil
}
