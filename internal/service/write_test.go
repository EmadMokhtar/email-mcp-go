package service

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/confirm"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/outbox"
	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
	"github.com/EmadMokhtar/email-mcp-go/internal/provider/imapsmtp"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

// ---------------------------------------------------------------- helpers

// testClock is a clock that a test moves by hand. Every part of a writeEnv uses
// it.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// sentCall is one call of the fake sender of the outbox.
type sentCall struct {
	Account account.AccountID
	Msg     *mail.RawMessage
	Draft   mail.Ref
}

// fakeSender is the Sender of the outbox in the tests. It records each message.
type fakeSender struct {
	mu    sync.Mutex
	calls []sentCall
	err   error
	// started gets a value when a send starts, and the send then waits until
	// release is closed, when a test set them.
	started chan struct{}
	release chan struct{}
}

func (s *fakeSender) Send(_ context.Context, _ account.TenantID, acct account.AccountID, msg *mail.RawMessage, draft mail.Ref) error {
	// A test that sets release holds the send until it closes the channel. It
	// sets both fields before the first send.
	if s.release != nil {
		s.started <- struct{}{}
		<-s.release
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, sentCall{Account: acct, Msg: msg, Draft: draft})
	return s.err
}

func (s *fakeSender) sent() []sentCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

// moveCall is one call of recBackend.Move.
type moveCall struct {
	Refs   []mail.Ref
	Folder string
}

// recBackend is a mail backend for the write tests. It records the calls that
// change mail. A method that it does not define panics (the embedded interface
// is nil), so a test sees at once a call that the service should not make.
type recBackend struct {
	mail.Backend

	mu      sync.Mutex
	folders []mail.Folder
	// originals are the messages that GetMessage returns. A test that needs a
	// message to change between two calls sets nextOriginal.
	originals    map[mail.Ref]*mail.Message
	nextOriginal func(ref mail.Ref, call int) *mail.Message
	getCalls     int
	raws         map[mail.Ref][]byte
	rawMax       int64
	errs         map[string]error // the error of a method, by name
	// attachments are the attachments that GetAttachment returns, by id.
	attachments map[string]*mail.Attachment

	flags     []mail.FlagChange
	tagged    [][]string
	moved     []moveCall
	trashed   [][]mail.Ref
	saved     []*mail.RawMessage
	discarded []mail.Ref
	created   []string
}

// newRecBackend returns a backend with the usual folders. The Trash folder has
// a name that is not "Trash", as on many servers.
func newRecBackend() *recBackend {
	return &recBackend{
		folders: []mail.Folder{
			{Name: "INBOX", Alias: mail.AliasInbox, Delimiter: '/'},
			{Name: "Archive", Alias: mail.AliasArchive, Delimiter: '/'},
			{Name: "Deleted Items", Alias: mail.AliasTrash, Trash: true, Delimiter: '/'},
			{Name: "Drafts", Alias: mail.AliasDrafts, Delimiter: '/'},
			{Name: "Projects", Delimiter: '/'},
		},
		originals:   map[mail.Ref]*mail.Message{},
		raws:        map[mail.Ref][]byte{},
		errs:        map[string]error{},
		attachments: map[string]*mail.Attachment{},
	}
}

func (b *recBackend) GetAttachment(_ context.Context, _ mail.Ref, id string, _ int64) (*mail.Attachment, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	att, ok := b.attachments[id]
	if !ok {
		return nil, mail.ErrNotFound
	}
	return att, nil
}

// addMessage stores a message for GetMessage and GetRaw. draft says whether it
// has the draft state.
func (b *recBackend) addMessage(ref mail.Ref, raw []byte, draft bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.raws[ref] = raw
	b.originals[ref] = &mail.Message{Summary: mail.Summary{Ref: ref, Draft: draft}}
}

func (b *recBackend) setErr(method string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.errs[method] = err
}

func (b *recBackend) ListFolders(context.Context) ([]mail.Folder, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.folders), b.errs["ListFolders"]
}

func (b *recBackend) GetMessage(_ context.Context, ref mail.Ref, _ mail.BodyOptions) (*mail.Message, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.getCalls++
	if err := b.errs["GetMessage"]; err != nil {
		return nil, err
	}
	if b.nextOriginal != nil {
		return b.nextOriginal(ref, b.getCalls), nil
	}
	m, ok := b.originals[ref]
	if !ok {
		return nil, mail.ErrNotFound
	}
	return m, nil
}

func (b *recBackend) GetRaw(_ context.Context, ref mail.Ref, maxBytes int64) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rawMax = maxBytes
	raw, ok := b.raws[ref]
	if !ok {
		return nil, mail.ErrNotFound
	}
	return raw, nil
}

func (b *recBackend) SetFlags(_ context.Context, _ []mail.Ref, change mail.FlagChange) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.flags = append(b.flags, change)
	return b.errs["SetFlags"]
}

func (b *recBackend) Tag(_ context.Context, _ []mail.Ref, add, remove []string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tagged = append(b.tagged, append(slices.Clone(add), remove...))
	return b.errs["Tag"]
}

func (b *recBackend) CreateFolder(_ context.Context, name, parent string) (mail.Folder, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.created = append(b.created, "folder:"+name)
	if err := b.errs["CreateFolder"]; err != nil {
		return mail.Folder{}, err
	}
	if parent != "" {
		name = parent + "/" + name
	}
	return mail.Folder{Name: name}, nil
}

func (b *recBackend) CreateTag(_ context.Context, name string) (mail.Tag, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.created = append(b.created, "tag:"+name)
	return mail.Tag{Name: name}, b.errs["CreateTag"]
}

func (b *recBackend) Move(_ context.Context, refs []mail.Ref, folder string) ([]mail.Ref, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.moved = append(b.moved, moveCall{Refs: slices.Clone(refs), Folder: folder})
	if err := b.errs["Move"]; err != nil {
		return nil, err
	}
	out := make([]mail.Ref, len(refs))
	for i, r := range refs {
		out[i] = mail.Ref(folder + ":" + string(r))
	}
	return out, nil
}

func (b *recBackend) Trash(_ context.Context, refs []mail.Ref) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.trashed = append(b.trashed, slices.Clone(refs))
	return b.errs["Trash"]
}

func (b *recBackend) SaveDraft(_ context.Context, msg *mail.RawMessage) (mail.Ref, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.saved = append(b.saved, msg)
	if err := b.errs["SaveDraft"]; err != nil {
		return "", err
	}
	return mail.Ref(fmt.Sprintf("draft-%d", len(b.saved))), nil
}

func (b *recBackend) DiscardDraft(_ context.Context, ref mail.Ref) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.discarded = append(b.discarded, ref)
	return b.errs["DiscardDraft"]
}

func (b *recBackend) trashCalls() [][]mail.Ref {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.trashed)
}

func (b *recBackend) moveCalls() []moveCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.moved)
}

// failingAuditStore is an audit store whose writes fail.
type failingAuditStore struct{}

func (failingAuditStore) CreateAudit(context.Context, *store.AuditRow) error {
	return errors.New("disk full")
}

func (failingAuditStore) ListAudit(context.Context, account.TenantID, store.AuditFilter) ([]*store.AuditRow, error) {
	return nil, nil
}

func (failingAuditStore) PruneAudit(context.Context, time.Time) (int, error) { return 0, nil }

// rawDB opens the database file of the fixture on its own connection, for a
// test that reads a table that the store has no call for. It is closed when the
// test ends.
func (f *fixture) rawDB() *sql.DB {
	f.t.Helper()
	uriPath := filepath.ToSlash(filepath.Join(f.dir, "email-mcp.db"))
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	db, err := sql.Open("sqlite", "file:"+uriPath+"?_pragma=busy_timeout(5000)")
	require.NoError(f.t, err)
	f.t.Cleanup(func() { require.NoError(f.t, db.Close()) })
	return db
}

// syncBuffer is a bytes.Buffer that many goroutines may write to.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// writeEnv is a service with every part that the write use cases need: the real
// store, vault, limiter, confirm manager, outbox and audit log of a fixture, a
// recording backend, a fake sender and a clock that the test moves.
type writeEnv struct {
	t       *testing.T
	f       *fixture
	svc     *Service
	backend *recBackend
	factory *fakeFactory
	sender  *fakeSender
	outbox  *outbox.Outbox
	confirm *confirm.Manager
	audit   *audit.Log
	links   *fakeLinks
	clock   *testClock
	logs    *syncBuffer
}

// envOption changes the parts of a writeEnv before the service is built.
type envOption func(*Deps)

// newWriteEnv builds a writeEnv. Accounts are added with env.account.
func newWriteEnv(t *testing.T, opts ...envOption) *writeEnv {
	t.Helper()
	f := newFixture(t)
	clock := &testClock{now: testStart}
	sender := &fakeSender{}
	ob := outbox.New(f.store, f.vault, sender.Send, clock.Now)
	cm := confirm.NewManager(f.store, f.vault, f.signer, clock.Now)
	log := audit.New(f.store, f.vault, clock.Now)
	links := &fakeLinks{}
	backend := newRecBackend()
	factory := &fakeFactory{backend: backend}
	logs := &syncBuffer{}
	d := Deps{
		Store:    f.store,
		Vault:    f.vault,
		Signer:   f.signer,
		Backends: factory,
		Limiter:  &policy.Limiter{Store: f.store, Now: clock.Now},
		Confirm:  cm,
		Outbox:   ob,
		Audit:    log,
		Links:    links,
		Now:      clock.Now,
		Logger:   slog.New(slog.NewTextHandler(logs, nil)),
	}
	for _, o := range opts {
		o(&d)
	}
	return &writeEnv{
		t: t, f: f, svc: New(d), backend: backend, factory: factory, sender: sender, outbox: ob,
		confirm: cm, audit: log, links: links, clock: clock, logs: logs,
	}
}

// account adds an account of the local tenant with the address
// me@example.com. With no change, it has the defaults of a new account: Mail 4,
// both confirmations on, the default limits.
func (e *writeEnv) account(change ...func(*account.Account)) *account.Account {
	e.t.Helper()
	return e.f.addAccount(e.f.tenant, "me@example.com", offlineSettings(), "pw", change...)
}

// level returns a change that sets the mail level.
func level(l account.Level) func(*account.Account) {
	return func(a *account.Account) { a.Levels[account.AreaMail] = l }
}

// autonomy returns a change that turns both confirmations off.
func autonomy(a *account.Account) {
	a.ConfirmSends = false
	a.ConfirmDeletes = false
}

// ctx returns a context with the principal of the local tenant.
func (e *writeEnv) ctx() context.Context { return e.f.ctx() }

// entries returns the audit entries of the tenant, newest first.
func (e *writeEnv) entries() []audit.Entry {
	e.t.Helper()
	list, err := e.audit.List(context.Background(), e.f.tenant, audit.Filter{Limit: 1000})
	require.NoError(e.t, err)
	return list
}

// lastEntry returns the newest audit entry.
func (e *writeEnv) lastEntry() audit.Entry {
	e.t.Helper()
	list := e.entries()
	require.NotEmpty(e.t, list, "an audit entry was expected")
	return list[0]
}

// pending returns a pending action of the tenant.
func (e *writeEnv) pending(id string) *confirm.Pending {
	e.t.Helper()
	p, _, err := e.confirm.Get(context.Background(), e.f.tenant, id)
	require.NoError(e.t, err)
	return p
}

// waitingActions returns the actions of the tenant that wait for the user.
func (e *writeEnv) waitingActions() []*confirm.Pending {
	e.t.Helper()
	list, err := e.confirm.ListPending(context.Background(), e.f.tenant)
	require.NoError(e.t, err)
	return list
}

// requireEntry checks the fields of an audit entry that the tests care about.
func requireEntry(t *testing.T, e audit.Entry, tool string, outcome audit.Outcome, detail string) {
	t.Helper()
	require.Equal(t, tool, e.Tool, "tool")
	require.Equal(t, outcome, e.Outcome, "outcome of %s", e.Tool)
	require.Equal(t, detail, e.Detail, "detail of %s", e.Tool)
}

// writeUses lists every write use case of one account, so a test can check
// them all with the same account.
func writeUses(svc *Service, id account.AccountID) map[string]func(ctx context.Context) error {
	refs := []mail.Ref{"r1"}
	c := mail.Compose{To: []string{"bob@example.com"}, Subject: "Hi", Body: "Hello"}
	return map[string]func(ctx context.Context) error{
		"SetFlags": func(ctx context.Context) error {
			return svc.SetFlags(ctx, id, refs, mail.FlagChange{Seen: new(true)})
		},
		"CreateFolder": func(ctx context.Context) error { _, err := svc.CreateFolder(ctx, id, "New", ""); return err },
		"CreateTag":    func(ctx context.Context) error { _, err := svc.CreateTag(ctx, id, "urgent"); return err },
		"Tag":          func(ctx context.Context) error { return svc.Tag(ctx, id, refs, []string{"urgent"}, nil) },
		"Move":         func(ctx context.Context) error { _, err := svc.Move(ctx, id, refs, "Archive"); return err },
		"CreateDraft":  func(ctx context.Context) error { _, err := svc.CreateDraft(ctx, id, c); return err },
		"DiscardDraft": func(ctx context.Context) error { return svc.DiscardDraft(ctx, id, "d1") },
		"Send": func(ctx context.Context) error {
			_, _, err := svc.Send(ctx, id, &c, nil, ConfirmInBand, nil)
			return err
		},
		"CancelSend": func(ctx context.Context) error { return svc.CancelSend(ctx, id, "s1") },
		"Trash": func(ctx context.Context) error {
			_, _, err := svc.Trash(ctx, id, refs, ConfirmInBand, nil)
			return err
		},
	}
}

// ---------------------------------------------------------------- access

func TestWriteRefusedAtLevel0(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailOff))
	factory := env.svc.d.Backends.(*fakeFactory)

	for name, use := range writeUses(env.svc, a.ID) {
		t.Run(name, func(t *testing.T) {
			err := use(env.ctx())
			var perm *PermissionError
			require.ErrorAs(t, err, &perm)
			require.False(t, perm.Decision.Allowed)
		})
	}
	require.Zero(t, factory.calls(), "a refused write must not reach a backend")
	require.Empty(t, env.sender.sent())
	require.Empty(t, env.waitingActions())
	for _, e := range env.entries() {
		require.Equal(t, audit.OutcomeRefused, e.Outcome, e.Tool)
		require.Equal(t, "level_too_low", e.Detail, e.Tool)
		require.Equal(t, a.ID, e.Account)
	}
	require.Len(t, env.entries(), len(writeUses(env.svc, a.ID)), "every refused write is audited")
}

func TestWriteOnAccountOfOtherTenantNotFound(t *testing.T) {
	env := newWriteEnv(t)
	env.f.addTenant("other")
	theirs := env.f.addAccount("other", "them@example.com", offlineSettings(), "pw", level(account.MailDelete))
	factory := env.svc.d.Backends.(*fakeFactory)

	for name, use := range writeUses(env.svc, theirs.ID) {
		t.Run(name, func(t *testing.T) {
			require.ErrorIs(t, use(env.ctx()), ErrAccountNotFound)
		})
	}
	require.Zero(t, factory.calls())
	for _, e := range env.entries() {
		require.Empty(t, e.Account, "the entry must not name the account of another tenant")
		require.Equal(t, "account_not_found", e.Detail)
	}
}

func TestWriteWithoutPrincipalRefused(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailDelete))
	for name, use := range writeUses(env.svc, a.ID) {
		t.Run(name, func(t *testing.T) {
			require.ErrorIs(t, use(context.Background()), ErrNoPrincipal)
		})
	}
}

// ---------------------------------------------------------------- organize

func TestOrganizeNeedsLevel2(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailRead))

	err := env.svc.SetFlags(env.ctx(), a.ID, []mail.Ref{"r1"}, mail.FlagChange{Seen: new(true)})
	var perm *PermissionError
	require.ErrorAs(t, err, &perm)
	require.Equal(t, account.MailOrganize, perm.Decision.Required)
	requireEntry(t, env.lastEntry(), "set_flags", audit.OutcomeRefused, "level_too_low")
}

func TestOrganizeCallsTheBackendAndAudits(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailOrganize))
	ctx := env.ctx()
	refs := []mail.Ref{"r1", "r2"}

	require.NoError(t, env.svc.SetFlags(ctx, a.ID, refs, mail.FlagChange{Flagged: new(true)}))
	requireEntry(t, env.lastEntry(), "set_flags", audit.OutcomeOK, "")
	require.Equal(t, 2, env.lastEntry().Count)

	folder, err := env.svc.CreateFolder(ctx, a.ID, "Taxes", "Projects")
	require.NoError(t, err)
	require.Equal(t, "Projects/Taxes", folder.Name)
	requireEntry(t, env.lastEntry(), "create_folder", audit.OutcomeOK, "")

	tag, err := env.svc.CreateTag(ctx, a.ID, "urgent")
	require.NoError(t, err)
	require.Equal(t, "urgent", tag.Name)
	requireEntry(t, env.lastEntry(), "create_tag", audit.OutcomeOK, "")

	require.NoError(t, env.svc.Tag(ctx, a.ID, refs, []string{"urgent"}, []string{"later"}))
	requireEntry(t, env.lastEntry(), "tag_messages", audit.OutcomeOK, "")

	moved, err := env.svc.Move(ctx, a.ID, refs, "Archive")
	require.NoError(t, err)
	require.Equal(t, []mail.Ref{"Archive:r1", "Archive:r2"}, moved)
	requireEntry(t, env.lastEntry(), "move_messages", audit.OutcomeOK, "")
	require.Equal(t, []moveCall{{Refs: refs, Folder: "Archive"}}, env.backend.moveCalls(),
		"the folder goes to the backend as the agent gave it")

	env.backend.setErr("CreateFolder", fmt.Errorf("%w: exists", mail.ErrConflict))
	_, err = env.svc.CreateFolder(ctx, a.ID, "Taxes", "")
	require.ErrorIs(t, err, mail.ErrConflict)
	requireEntry(t, env.lastEntry(), "create_folder", audit.OutcomeError, "conflict")
}

// A flag change can only say "seen" and "flagged". There is no way to ask for
// \Deleted or any other flag that removes mail, so SetFlags at level 2 cannot
// delete. If someone adds a field to mail.FlagChange, this test fails and makes
// them check that the new flag cannot delete mail.
func TestSetFlagsCannotDelete(t *testing.T) {
	typ := reflect.TypeFor[mail.FlagChange]()
	var fields []string
	for i := range typ.NumField() {
		f := typ.Field(i)
		require.Equal(t, reflect.TypeFor[*bool](), f.Type, f.Name)
		fields = append(fields, f.Name)
	}
	require.Equal(t, []string{"Seen", "Flagged"}, fields)

	env := newWriteEnv(t)
	a := env.account(level(account.MailOrganize))
	change := mail.FlagChange{Seen: new(false), Flagged: new(true)}
	require.NoError(t, env.svc.SetFlags(env.ctx(), a.ID, []mail.Ref{"r1"}, change))
	require.Equal(t, []mail.FlagChange{change}, env.backend.flags, "the change reaches the backend as it is")
}

func TestOrganizePartialChangeIsReported(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailOrganize))
	partial := fmt.Errorf("imap: 1 of 2 messages were already moved when this error happened: %w: %w",
		mail.ErrPartial, errors.New("broken pipe"))
	env.backend.setErr("Move", partial)
	env.backend.setErr("SetFlags", partial)

	_, err := env.svc.Move(env.ctx(), a.ID, []mail.Ref{"r1", "r2"}, "Archive")
	require.ErrorIs(t, err, mail.ErrPartial)
	require.Contains(t, err.Error(), "Search again")
	require.Contains(t, err.Error(), "do not repeat")
	requireEntry(t, env.lastEntry(), "move_messages", audit.OutcomeError, "partial")

	err = env.svc.SetFlags(env.ctx(), a.ID, []mail.Ref{"r1", "r2"}, mail.FlagChange{Seen: new(true)})
	require.ErrorIs(t, err, mail.ErrPartial)
	requireEntry(t, env.lastEntry(), "set_flags", audit.OutcomeError, "partial")
}

// ---------------------------------------------------------------- move to Trash

func TestMoveToTrashRefusedBelowLevel5(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailOrganize), autonomy)

	_, err := env.svc.Move(env.ctx(), a.ID, []mail.Ref{"r1"}, mail.AliasTrash)

	require.ErrorIs(t, err, ErrMoveToTrash)
	require.Contains(t, err.Error(), "trash_messages")
	require.Empty(t, env.backend.moveCalls())
	requireEntry(t, env.lastEntry(), "move_messages", audit.OutcomeRefused, "move_to_trash")
}

// A move to Trash is refused at every level, so the Trash confirmation and the
// Trash limit cannot be skipped. The Trash folder is found the way the backend
// finds it for its own Trash: the folder with the Trash alias. Its name, in any
// case, counts too, because some servers do not care about case.
func TestMoveToTrashRefusedAtEveryLevel(t *testing.T) {
	for _, lvl := range []account.Level{account.MailOrganize, account.MailDraft, account.MailSend, account.MailDelete} {
		for _, target := range []string{mail.AliasTrash, "Deleted Items", "deleted items", "DELETED ITEMS"} {
			t.Run(fmt.Sprintf("level %d to %q", lvl, target), func(t *testing.T) {
				env := newWriteEnv(t)
				a := env.account(level(lvl), autonomy)

				_, err := env.svc.Move(env.ctx(), a.ID, []mail.Ref{"r1"}, target)

				require.ErrorIs(t, err, ErrMoveToTrash)
				require.Empty(t, env.backend.moveCalls())
			})
		}
	}
}

func TestMoveToAnAliasOfTheTrashFolderRefused(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailDelete), autonomy)
	// One folder is both Junk and Trash on this server. The backend's list
	// shows it as Trash. A move to @junk must not reach it.
	env.backend.folders = []mail.Folder{
		{Name: "INBOX", Alias: mail.AliasInbox},
		{Name: "Bin", Alias: mail.AliasTrash, Trash: true},
	}

	_, err := env.svc.Move(env.ctx(), a.ID, []mail.Ref{"r1"}, mail.AliasJunk)

	require.ErrorIs(t, err, mail.ErrNotFound, "an alias that the list does not show is not passed on")
	require.Empty(t, env.backend.moveCalls())
}

// A server can have more than one folder with the \Trash attribute. Only one
// has the alias, but a move to any of them is a move to Trash.
func TestMoveToAnyTrashFolderRefused(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailDelete), autonomy)
	env.backend.folders = []mail.Folder{
		{Name: "INBOX", Alias: mail.AliasInbox},
		{Name: "Deleted Items", Alias: mail.AliasTrash, Trash: true},
		{Name: "Trash", Trash: true},
		{Name: "Projects"},
	}
	for _, target := range []string{"Deleted Items", "Trash", "TRASH"} {
		_, err := env.svc.Move(env.ctx(), a.ID, []mail.Ref{"r1"}, target)
		require.ErrorIs(t, err, ErrMoveToTrash, target)
	}
	require.Empty(t, env.backend.moveCalls())

	_, err := env.svc.Move(env.ctx(), a.ID, []mail.Ref{"r1"}, "Projects")
	require.NoError(t, err)
}

func TestMoveToOtherFoldersAllowed(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailOrganize))
	// "Deleted Items.Old" and "Deleted ItemsOld" are not inside Trash: the
	// delimiter of this server is "/".
	for _, target := range []string{"", mail.AliasInbox, "inbox", mail.AliasArchive, "Projects", "Projects/Deleted Items",
		"Deleted Items.Old", "Deleted ItemsOld"} {
		_, err := env.svc.Move(env.ctx(), a.ID, []mail.Ref{"r1"}, target)
		require.NoError(t, err, target)
	}
	require.Len(t, env.backend.moveCalls(), 8)
}

// A folder inside Trash is Trash too: mail clients (Thunderbird, for example)
// and server purge rules empty Trash with its sub-folders. So a move there is a
// delete in disguise, and it would skip the Trash confirmation and the Trash
// limit. Controller ruling for the final review of M0a (spec section 7.2: delete
// needs Mail 5): it is refused at every level, like a move to Trash itself. This
// reverses the earlier test row that allowed "Deleted Items/Old" at Mail 2.
func TestMoveIntoAFolderInsideTrashRefused(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailDelete), autonomy)
	for _, target := range []string{"Deleted Items/Old", "deleted items/Old/Deeper", "DELETED ITEMS/x"} {
		_, err := env.svc.Move(env.ctx(), a.ID, []mail.Ref{"r1"}, target)
		require.ErrorIs(t, err, ErrMoveToTrash, target)
	}

	// Every Trash folder counts, with its own delimiter, also one inside INBOX.
	env.backend.folders = []mail.Folder{
		{Name: "INBOX", Alias: mail.AliasInbox, Delimiter: '.'},
		{Name: "INBOX.Trash", Alias: mail.AliasTrash, Trash: true, Delimiter: '.'},
		{Name: "Bin", Trash: true, Delimiter: '.'},
	}
	for _, target := range []string{"INBOX.Trash.x", "inbox.trash.x.y", "Bin.Old"} {
		_, err := env.svc.Move(env.ctx(), a.ID, []mail.Ref{"r1"}, target)
		require.ErrorIs(t, err, ErrMoveToTrash, target)
	}
	require.Empty(t, env.backend.moveCalls())

	_, err := env.svc.Move(env.ctx(), a.ID, []mail.Ref{"r1"}, "INBOX.Trashcan")
	require.NoError(t, err, "a folder whose name only starts with the name of Trash is not inside it")
}

// A folder made inside Trash would be a place to move mail into Trash with
// move_messages. So create_folder refuses a parent that is a Trash folder or a
// folder inside one, before the backend is called (same ruling as above).
func TestCreateFolderInsideTrashRefused(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailDelete), autonomy)
	for _, parent := range []string{mail.AliasTrash, "Deleted Items", "deleted items", "Deleted Items/Old"} {
		_, err := env.svc.CreateFolder(env.ctx(), a.ID, "x", parent)
		require.ErrorIs(t, err, ErrMoveToTrash, parent)
		requireEntry(t, env.lastEntry(), "create_folder", audit.OutcomeRefused, "move_to_trash")
	}
	env.backend.mu.Lock()
	created := slices.Clone(env.backend.created)
	env.backend.mu.Unlock()
	require.Empty(t, created, "the backend is not called")

	for _, parent := range []string{"", "Projects", mail.AliasArchive} {
		_, err := env.svc.CreateFolder(env.ctx(), a.ID, "x", parent)
		require.NoError(t, err, parent)
	}
}

func TestMoveWhenTheAccountHasNoTrashFolder(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailOrganize))
	env.backend.folders = []mail.Folder{{Name: "INBOX", Alias: mail.AliasInbox}, {Name: "Trash"}}

	_, err := env.svc.Move(env.ctx(), a.ID, []mail.Ref{"r1"}, mail.AliasTrash)
	require.ErrorIs(t, err, ErrMoveToTrash, "@trash is refused even when no folder has the alias")

	// A folder that is only called Trash is still refused (review of PR #3,
	// item 1): on a server without SPECIAL-USE it is the user's real Trash, and
	// a move there would skip the Trash confirmation and the Trash limit.
	_, err = env.svc.Move(env.ctx(), a.ID, []mail.Ref{"r1"}, "Trash")
	require.ErrorIs(t, err, ErrMoveToTrash)
	require.Empty(t, env.backend.moveCalls())
}

// A server without SPECIAL-USE flags no folder as Trash, but the user's Trash
// is still there under a well-known name. A move to such a name, or to a folder
// inside it, is refused whatever the flags say (review of PR #3, item 1).
func TestMoveToAnUnflaggedTrashNameRefused(t *testing.T) {
	slash := []mail.Folder{
		{Name: "INBOX", Alias: mail.AliasInbox, Delimiter: '/'},
		{Name: "Trash", Delimiter: '/'},
		{Name: "Trash/Old", Delimiter: '/'},
		{Name: "Deleted Items", Delimiter: '/'},
		{Name: "Projects", Delimiter: '/'},
	}
	dot := []mail.Folder{
		{Name: "INBOX", Alias: mail.AliasInbox, Delimiter: '.'},
		{Name: "INBOX.Trash", Delimiter: '.'},
		{Name: "INBOX.Trash.Old", Delimiter: '.'},
		{Name: "INBOX.Projects", Delimiter: '.'},
	}
	for _, tc := range []struct {
		folders []mail.Folder
		target  string
	}{
		{slash, "Trash"},
		{slash, "trash"},
		{slash, "Trash/Old"},
		{slash, "Trash/Not/Listed"},
		{slash, "deleted items"},
		{slash, "Deleted Items/x"},
		{slash, "Deleted"},
		{slash, "DELETED MESSAGES"},
		{slash, "Bin"},
		{slash, "rubbish"},
		{slash, "INBOX/Trash"},
		{dot, "INBOX.Trash"},
		{dot, "inbox.trash"},
		{dot, "INBOX.Trash.Old"},
		{dot, "Trash"},
		{dot, "Trash.Old"},
		{dot, "INBOX.Bin"},
		// A folder with no hierarchy: only the bare name counts.
		{[]mail.Folder{{Name: "INBOX", Alias: mail.AliasInbox}, {Name: "Rubbish"}}, "Rubbish"},
	} {
		t.Run(tc.target, func(t *testing.T) {
			env := newWriteEnv(t)
			a := env.account(level(account.MailDelete), autonomy)
			env.backend.folders = tc.folders

			_, err := env.svc.Move(env.ctx(), a.ID, []mail.Ref{"r1"}, tc.target)

			require.ErrorIs(t, err, ErrMoveToTrash)
			require.Empty(t, env.backend.moveCalls())
		})
	}
}

// The name list matches a whole part of the name only, not a prefix: a folder
// such as "Trash Talk" or "Binders" is a normal folder.
func TestMoveToANameThatOnlyLooksLikeTrashAllowed(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailOrganize))
	env.backend.folders = []mail.Folder{
		{Name: "INBOX", Alias: mail.AliasInbox, Delimiter: '/'},
		{Name: "Trash Talk", Delimiter: '/'},
		{Name: "Binders", Delimiter: '/'},
		{Name: "Projects", Delimiter: '/'},
		{Name: "Projects/Trash", Delimiter: '/'},
	}
	targets := []string{"Trash Talk", "Binders", "Trashcan", "My Trash", "Deleted Items Old", "Projects/Trash",
		"Projects/Bin", "INBOX/Projects", "Trash.Old"}
	for _, target := range targets {
		_, err := env.svc.Move(env.ctx(), a.ID, []mail.Ref{"r1"}, target)
		require.NoError(t, err, target)
	}
	require.Len(t, env.backend.moveCalls(), len(targets))
}

// create_folder uses the same check as move_messages for its parent, so a
// parent under an unflagged Trash name is refused too.
func TestCreateFolderInsideAnUnflaggedTrashRefused(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailDelete), autonomy)
	env.backend.folders = []mail.Folder{
		{Name: "INBOX", Alias: mail.AliasInbox, Delimiter: '.'},
		{Name: "Trash", Delimiter: '.'},
		{Name: "INBOX.Deleted Items", Delimiter: '.'},
		{Name: "Projects", Delimiter: '.'},
	}
	for _, parent := range []string{"Trash", "trash", "Trash.Old", "INBOX.Deleted Items", "inbox.deleted items"} {
		_, err := env.svc.CreateFolder(env.ctx(), a.ID, "x", parent)
		require.ErrorIs(t, err, ErrMoveToTrash, parent)
		requireEntry(t, env.lastEntry(), "create_folder", audit.OutcomeRefused, "move_to_trash")
	}
	env.backend.mu.Lock()
	created := slices.Clone(env.backend.created)
	env.backend.mu.Unlock()
	require.Empty(t, created, "the backend is not called")

	_, err := env.svc.CreateFolder(env.ctx(), a.ID, "x", "Projects")
	require.NoError(t, err)
}

// ---------------------------------------------------------------- a refused login

// When the mail server refuses the stored login (an app password was revoked),
// the service marks the account as needing a reconnect. The next call is then
// refused before it reaches the server (ReauthError), and list_accounts shows
// needs_reauth. This holds for reads and for writes.
func TestARefusedLoginMarksTheAccountForAReconnect(t *testing.T) {
	refused := fmt.Errorf("imap: login failed [AUTHENTICATIONFAILED]: %w", imapsmtp.ErrAuthFailed)
	for name, call := range map[string]func(env *writeEnv, id account.AccountID) error{
		"a read": func(env *writeEnv, id account.AccountID) error {
			_, err := env.svc.ListFolders(env.ctx(), id)
			return err
		},
		"a write": func(env *writeEnv, id account.AccountID) error {
			return env.svc.SetFlags(env.ctx(), id, []mail.Ref{"r1"}, mail.FlagChange{Seen: new(true)})
		},
	} {
		t.Run(name, func(t *testing.T) {
			env := newWriteEnv(t)
			a := env.account(level(account.MailOrganize))
			env.backend.setErr("ListFolders", refused)
			env.backend.setErr("SetFlags", refused)

			err := call(env, a.ID)
			require.ErrorIs(t, err, imapsmtp.ErrAuthFailed)
			require.Equal(t, account.StatusNeedsReauth, env.f.reload(a).Status)

			err = call(env, a.ID)
			var reauth *ReauthError
			require.ErrorAs(t, err, &reauth, "the next call does not reach the server")
		})
	}
}

// reconnectingBackend is a backend whose ListFolders runs during a reconnect:
// during runs (the reconnect commits) while the call is in progress, and then
// the call fails with a refused login, as a call on the old password does.
type reconnectingBackend struct {
	mail.Backend
	during func()
}

func (b *reconnectingBackend) ListFolders(context.Context) ([]mail.Folder, error) {
	if b.during != nil {
		b.during()
	}
	return nil, fmt.Errorf("imap: login failed [AUTHENTICATIONFAILED]: %w", imapsmtp.ErrAuthFailed)
}

// A call that started on the old backend before a reconnect committed, and then
// fails with a refused login, does not mark the account: the reconnect saved a
// password that works, and the mark would undo it. A refused login on the
// current credentials still marks the account.
func TestARefusedLoginOfACallFromBeforeAReconnectDoesNotMarkTheAccount(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailOrganize))
	ctx := context.Background()
	backend := &reconnectingBackend{during: func() {
		version, sealed, err := env.f.vault.Encrypt(ctx, a.TenantID, secretAAD(a), []byte("pw-new"))
		require.NoError(t, err)
		require.NoError(t, env.f.store.ReconnectAccount(ctx, a.TenantID, a.ID, store.AccountReconnect{
			Tested: *a.Mail, SecretKind: passwordSecret, KeyVersion: version, Sealed: sealed,
		}))
	}}
	env.factory.mu.Lock()
	env.factory.byAccount = map[account.AccountID]mail.Backend{a.ID: backend}
	env.factory.mu.Unlock()

	_, err := env.svc.ListFolders(env.ctx(), a.ID)

	require.ErrorIs(t, err, imapsmtp.ErrAuthFailed)
	require.Equal(t, account.StatusOK, env.f.reload(a).Status, "the reconnect stays")
	require.Contains(t, env.logs.String(), "code=needs_reauth_skipped")

	// The next call reads the account after the reconnect. Its refused login is
	// about the credentials of now, so it marks the account.
	backend.during = nil
	_, err = env.svc.ListFolders(env.ctx(), a.ID)
	require.ErrorIs(t, err, imapsmtp.ErrAuthFailed)
	require.Equal(t, account.StatusNeedsReauth, env.f.reload(a).Status)
}

// secretAAD is the associated data of the password of an account.
func secretAAD(a *account.Account) string {
	return secrets.AAD(string(a.TenantID), string(a.ID), passwordSecret)
}

// A busy server (INUSE or LIMIT) did not refuse the password, so the account
// stays connected. So does any other error.
func TestOtherBackendErrorsDoNotMarkTheAccount(t *testing.T) {
	for name, err := range map[string]error{
		"busy":    fmt.Errorf("imap: login failed [INUSE]: %w", imapsmtp.ErrServerBusy),
		"network": &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset")},
		"pin":     &imapsmtp.CertificateUnknownError{Host: "imap.example.com", Pinned: true},
	} {
		t.Run(name, func(t *testing.T) {
			env := newWriteEnv(t)
			a := env.account(level(account.MailOrganize))
			env.backend.setErr("ListFolders", err)

			_, got := env.svc.ListFolders(env.ctx(), a.ID)
			require.ErrorIs(t, got, err)
			require.Equal(t, account.StatusOK, env.f.reload(a).Status)
		})
	}
}

// ---------------------------------------------------------------- drafts

func TestDraftsNeedLevel3(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailOrganize))

	_, err := env.svc.CreateDraft(env.ctx(), a.ID, mail.Compose{To: []string{"bob@example.com"}, Body: "x"})
	var perm *PermissionError
	require.ErrorAs(t, err, &perm)
	require.Equal(t, account.MailDraft, perm.Decision.Required)

	require.ErrorAs(t, env.svc.DiscardDraft(env.ctx(), a.ID, "d1"), &perm)
	require.Empty(t, env.backend.saved)
	require.Empty(t, env.backend.discarded)
}

func TestCreateAndDiscardDraft(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailDraft))
	ctx := env.ctx()

	ref, err := env.svc.CreateDraft(ctx, a.ID, mail.Compose{
		To: []string{"bob@example.com"}, Bcc: []string{"carol@example.com"}, Subject: "Plan", Body: "Draft text",
	})
	require.NoError(t, err)
	require.Equal(t, mail.Ref("draft-1"), ref)
	require.Len(t, env.backend.saved, 1)
	saved := env.backend.saved[0]
	require.Equal(t, "me@example.com", saved.From)
	require.Contains(t, string(saved.Data), "Bcc: carol@example.com", "a draft keeps its Bcc header")
	requireEntry(t, env.lastEntry(), "create_draft", audit.OutcomeOK, "")

	require.NoError(t, env.svc.DiscardDraft(ctx, a.ID, ref))
	require.Equal(t, []mail.Ref{ref}, env.backend.discarded)
	requireEntry(t, env.lastEntry(), "discard_draft", audit.OutcomeOK, "")
}

// ---------------------------------------------------------------- trash

func TestTrashNeedsLevel5(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailSend), autonomy)

	_, _, err := env.svc.Trash(env.ctx(), a.ID, []mail.Ref{"r1"}, ConfirmInBand, nil)
	var perm *PermissionError
	require.ErrorAs(t, err, &perm)
	require.Equal(t, account.MailDelete, perm.Decision.Required)
	require.Empty(t, env.backend.trashCalls())
}

func TestTrashNeedsConfirmation(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailDelete)) // ConfirmDeletes is on
	ctx := env.ctx()
	refs := []mail.Ref{"r2", "r1", "r2"}

	res, needs, err := env.svc.Trash(ctx, a.ID, refs, ConfirmInBand, nil)
	require.NoError(t, err)
	require.Nil(t, res)
	require.NotNil(t, needs)
	require.NotEmpty(t, needs.RequestState)
	require.Equal(t, confirm.KindTrash, needs.Pending.Kind)
	require.Equal(t, 2, needs.Pending.Preview.Count, "the same message twice counts once")
	require.Equal(t, "me@example.com", needs.Pending.Preview.Account)
	require.Empty(t, env.backend.trashCalls(), "nothing moves before the user confirms")
	requireEntry(t, env.lastEntry(), "trash_messages", audit.OutcomePendingApproval, "confirm_deletes")

	res, needs, err = env.svc.Trash(ctx, a.ID, refs, ConfirmInBand, &ConfirmAnswer{RequestState: needs.RequestState, Accepted: true})
	require.NoError(t, err)
	require.Nil(t, needs)
	require.Equal(t, &TrashResult{Status: "done", Count: 2}, res)
	require.Equal(t, [][]mail.Ref{{"r1", "r2"}}, env.backend.trashCalls())
	requireEntry(t, env.lastEntry(), "trash_messages", audit.OutcomeOK, "confirmed_in_band")
	require.Equal(t, 2, env.lastEntry().Count)
}

// The person must see which messages go to Trash, not only how many. The first
// call reads the head of each message once and stores up to 20 lines (date,
// sender address, subject) in the encrypted preview. A message that is gone
// gets a line that says so. The answer does not read the messages again: the
// action is bound to its refs (folder, UIDVALIDITY and UID).
func TestTrashPreviewListsTheMessages(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailDelete)) // ConfirmDeletes is on
	date := time.Date(2026, 3, 10, 9, 30, 0, 0, time.UTC)
	refs := []mail.Ref{"gone"}
	for i := 1; i <= 21; i++ {
		ref := mail.Ref(fmt.Sprintf("m%02d", i))
		refs = append(refs, ref)
		env.backend.originals[ref] = &mail.Message{Summary: mail.Summary{
			Ref: ref, Date: date, Subject: fmt.Sprintf("Note %d", i),
			From: mail.Address{Name: "Your Bank", Email: "alerts@bank.example"},
		}}
	}
	env.backend.originals["m01"].Subject = "New sign-in \u202eto your bank\nTo: someone@example.com"
	env.backend.originals["m02"].Subject = strings.Repeat("s", 150)
	env.backend.originals["m03"].Subject = ""
	env.backend.originals["m03"].Date = time.Time{}

	_, needs, err := env.svc.Trash(env.ctx(), a.ID, refs, ConfirmInBand, nil)
	require.NoError(t, err)

	p := needs.Pending.Preview
	require.Equal(t, 22, p.Count)
	require.Len(t, p.Messages, 20, "at most 20 lines")
	require.Equal(t, "… and 2 more not shown", p.MoreMessagesText())
	when := date.Local().Format("2006-01-02 15:04 MST")
	require.Equal(t, "(not found: it may have been moved or deleted already)", p.Messages[0], "a gone message does not stop the request")
	require.Equal(t, when+" — alerts@bank.example — New sign-in to your bank To: someone@example.com", p.Messages[1],
		"cleaned, on one line, and the address without the display name")
	require.Equal(t, when+" — alerts@bank.example — "+strings.Repeat("s", 100)+"...", p.Messages[2], "a long subject is cut")
	require.Equal(t, "(no date) — alerts@bank.example — (no subject)", p.Messages[3])
	require.Equal(t, when+" — alerts@bank.example — Note 19", p.Messages[19])
	stored := env.pending(needs.Pending.ID)
	require.Equal(t, p.Messages, stored.Preview.Messages, "the lines are stored with the action")

	reads := env.backend.getCalls
	res, _, err := env.svc.Trash(env.ctx(), a.ID, refs, ConfirmInBand, &ConfirmAnswer{RequestState: needs.RequestState, Accepted: true})
	require.NoError(t, err)
	require.Equal(t, TrashDone, res.Status)
	require.Equal(t, reads, env.backend.getCalls, "the answer does not read the messages again")
	require.Len(t, env.backend.trashCalls()[0], 22)
}

// A message that cannot be read for another reason than "not found" (the
// server does not answer) stops the request: nothing is asked.
func TestTrashPreviewFailsWhenTheServerFails(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailDelete))
	down := &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset")}
	env.backend.setErr("GetMessage", down)

	_, needs, err := env.svc.Trash(env.ctx(), a.ID, []mail.Ref{"m1"}, ConfirmInBand, nil)

	require.ErrorIs(t, err, down)
	require.Nil(t, needs)
	require.Empty(t, env.waitingActions())
	require.Empty(t, env.backend.trashCalls())
}

func TestTrashDeclined(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailDelete))
	ctx := env.ctx()
	refs := []mail.Ref{"r1"}

	_, needs, err := env.svc.Trash(ctx, a.ID, refs, ConfirmInBand, nil)
	require.NoError(t, err)
	res, _, err := env.svc.Trash(ctx, a.ID, refs, ConfirmInBand, &ConfirmAnswer{RequestState: needs.RequestState})
	require.NoError(t, err)
	require.Equal(t, "declined", res.Status)
	require.Empty(t, env.backend.trashCalls())
	require.Equal(t, confirm.StateRejected, env.pending(needs.Pending.ID).State)
	requireEntry(t, env.lastEntry(), "trash_messages", audit.OutcomeDeclined, "declined")

	// The state cannot be used again.
	_, _, err = env.svc.Trash(ctx, a.ID, refs, ConfirmInBand, &ConfirmAnswer{RequestState: needs.RequestState, Accepted: true})
	require.ErrorIs(t, err, confirm.ErrNotPending)
	require.Empty(t, env.backend.trashCalls())
}

func TestTrashRetryWithOtherRefsRejected(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailDelete))
	ctx := env.ctx()

	_, needs, err := env.svc.Trash(ctx, a.ID, []mail.Ref{"r1"}, ConfirmInBand, nil)
	require.NoError(t, err)
	_, _, err = env.svc.Trash(ctx, a.ID, []mail.Ref{"r1", "r9"}, ConfirmInBand, &ConfirmAnswer{RequestState: needs.RequestState, Accepted: true})
	require.ErrorIs(t, err, confirm.ErrPayloadChanged)
	require.Empty(t, env.backend.trashCalls())
	requireEntry(t, env.lastEntry(), "trash_messages", audit.OutcomeRefused, "payload_changed")
	require.Equal(t, confirm.StatePending, env.pending(needs.Pending.ID).State, "a changed payload does not use the confirmation up")
}

func TestTrashWithoutConfirmationActsAtOnce(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailDelete), autonomy)

	res, needs, err := env.svc.Trash(env.ctx(), a.ID, []mail.Ref{"r1"}, ConfirmInBand, nil)
	require.NoError(t, err)
	require.Nil(t, needs)
	require.Equal(t, &TrashResult{Status: "done", Count: 1}, res)
	require.Len(t, env.backend.trashCalls(), 1)
	requireEntry(t, env.lastEntry(), "trash_messages", audit.OutcomeOK, "no_confirmation")
}

// A ref with a line break would split into two refs when an approved payload is
// read back, so the user would approve one message and lose two.
func TestTrashRefusesRefsThatCannotBeStoredSafely(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailDelete))
	for name, refs := range map[string][]mail.Ref{
		"no refs":    nil,
		"empty ref":  {""},
		"line break": {"r1\nr2"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := env.svc.Trash(env.ctx(), a.ID, refs, ConfirmInBand, nil)
			require.ErrorIs(t, err, ErrInvalidRequest)
		})
	}
	require.Empty(t, env.waitingActions())
	require.Empty(t, env.backend.trashCalls())
}

func TestTrashFallbackThenApprove(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailDelete))
	ctx := env.ctx()

	res, needs, err := env.svc.Trash(ctx, a.ID, []mail.Ref{"r1", "r2"}, ConfirmFallback, nil)
	require.NoError(t, err)
	require.Nil(t, needs)
	require.Equal(t, "pending_approval", res.Status)
	require.Equal(t, 2, res.Count)
	require.NotEmpty(t, res.ApprovalLink)
	require.NotContains(t, res.ApprovalLink, "token=")
	waiting := env.waitingActions()
	require.Len(t, waiting, 1)
	requireEntry(t, env.lastEntry(), "trash_messages", audit.OutcomePendingApproval, "confirm_deletes")

	require.NoError(t, env.svc.ApprovePending(ctx, waiting[0].ID))
	require.Equal(t, [][]mail.Ref{{"r1", "r2"}}, env.backend.trashCalls())
	require.Equal(t, confirm.StateDone, env.pending(waiting[0].ID).State)
	requireEntry(t, env.lastEntry(), "trash_messages", audit.OutcomeOK, "approved_on_web")

	require.ErrorIs(t, env.svc.ApprovePending(ctx, waiting[0].ID), confirm.ErrNotPending)
	require.Len(t, env.backend.trashCalls(), 1, "an approval acts once")
}

// A Trash approval needs no outbox and no vault: only a send does.
func TestTrashApprovalNeedsNoOutbox(t *testing.T) {
	env := newWriteEnv(t, func(d *Deps) { d.Outbox = nil })
	a := env.account(level(account.MailDelete))
	_, _, err := env.svc.Trash(env.ctx(), a.ID, []mail.Ref{"r1"}, ConfirmFallback, nil)
	require.NoError(t, err)
	id := env.waitingActions()[0].ID

	require.NoError(t, env.svc.ApprovePending(env.ctx(), id))
	require.Len(t, env.backend.trashCalls(), 1)
}

// An approval or a rejection that cannot be carried out is in the audit log
// too. With the action known, the entry names its tool and account. Without it,
// the entry is one of the tenant.
func TestApproveAndRejectFailuresAreAudited(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailDelete))
	ctx := env.ctx()
	_, _, err := env.svc.Trash(ctx, a.ID, []mail.Ref{"r1"}, ConfirmFallback, nil)
	require.NoError(t, err)
	id := env.waitingActions()[0].ID
	require.NoError(t, env.svc.RejectPending(ctx, id))

	// Each failed call adds exactly one entry.
	newEntry := func(call func() error, want error) audit.Entry {
		t.Helper()
		before := len(env.entries())
		require.ErrorIs(t, call(), want)
		after := env.entries()
		require.Len(t, after, before+1, "one entry for the failed call")
		return after[0]
	}

	last := newEntry(func() error { return env.svc.ApprovePending(ctx, id) }, confirm.ErrNotPending)
	requireEntry(t, last, "trash_messages", audit.OutcomeRefused, "not_pending")
	require.Equal(t, a.ID, last.Account)

	last = newEntry(func() error { return env.svc.RejectPending(ctx, id) }, confirm.ErrNotPending)
	requireEntry(t, last, "trash_messages", audit.OutcomeRefused, "not_pending")
	require.Equal(t, a.ID, last.Account)

	last = newEntry(func() error { return env.svc.ApprovePending(ctx, "no-such-action") }, confirm.ErrNotFound)
	requireEntry(t, last, "approve_pending", audit.OutcomeRefused, "not_found")
	require.Empty(t, last.Account)

	last = newEntry(func() error { return env.svc.RejectPending(ctx, "no-such-action") }, confirm.ErrNotFound)
	requireEntry(t, last, "reject_pending", audit.OutcomeRefused, "not_found")
	require.Empty(t, last.Account)
	require.Empty(t, env.backend.trashCalls())
}

func TestRejectPendingTrash(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailDelete))
	ctx := env.ctx()

	_, _, err := env.svc.Trash(ctx, a.ID, []mail.Ref{"r1"}, ConfirmFallback, nil)
	require.NoError(t, err)
	id := env.waitingActions()[0].ID

	require.NoError(t, env.svc.RejectPending(ctx, id))
	require.Equal(t, confirm.StateRejected, env.pending(id).State)
	requireEntry(t, env.lastEntry(), "trash_messages", audit.OutcomeDeclined, "rejected_on_web")
	require.ErrorIs(t, env.svc.ApprovePending(ctx, id), confirm.ErrNotPending)
	require.Empty(t, env.backend.trashCalls())
}

// ---------------------------------------------------------------- trash limits

func TestTrashLimitsAreCheckedBeforeAsking(t *testing.T) {
	t.Run("a limit of 0 turns Trash off", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account(level(account.MailDelete), func(a *account.Account) { a.Limits.TrashPerDay = 0 })

		_, needs, err := env.svc.Trash(env.ctx(), a.ID, []mail.Ref{"r1"}, ConfirmInBand, nil)
		require.ErrorIs(t, err, ErrLimit)
		require.Contains(t, err.Error(), "turned off")
		require.Nil(t, needs)
		require.Empty(t, env.waitingActions(), "the user is not asked for something that cannot happen")
		requireEntry(t, env.lastEntry(), "trash_messages", audit.OutcomeRefused, "limit_disabled")
	})
	t.Run("more than the day allows at once", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account(level(account.MailDelete), func(a *account.Account) { a.Limits.TrashPerDay = 2 })

		_, _, err := env.svc.Trash(env.ctx(), a.ID, []mail.Ref{"r1", "r2", "r3"}, ConfirmInBand, nil)
		require.ErrorIs(t, err, ErrLimit)
		require.Contains(t, err.Error(), "in one call")
		var rate *policy.RateLimitError
		require.NotErrorAs(t, err, &rate, "waiting would not help, so there is no 'try again'")
		require.Empty(t, env.waitingActions())
		requireEntry(t, env.lastEntry(), "trash_messages", audit.OutcomeRefused, "too_many_at_once")
	})
	t.Run("a full day gives the time to wait", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account(level(account.MailDelete), autonomy, func(a *account.Account) { a.Limits.TrashPerDay = 2 })

		_, _, err := env.svc.Trash(env.ctx(), a.ID, []mail.Ref{"r1", "r2"}, ConfirmInBand, nil)
		require.NoError(t, err)
		_, _, err = env.svc.Trash(env.ctx(), a.ID, []mail.Ref{"r3"}, ConfirmInBand, nil)
		var rate *policy.RateLimitError
		require.ErrorAs(t, err, &rate)
		require.Contains(t, err.Error(), "Try again in")
		require.Len(t, env.backend.trashCalls(), 1)
		requireEntry(t, env.lastEntry(), "trash_messages", audit.OutcomeRefused, "rate_limited")
	})
}

func TestDeclinedTrashUsesNoQuota(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailDelete), func(a *account.Account) { a.Limits.TrashPerDay = 1 })
	ctx := env.ctx()
	refs := []mail.Ref{"r1"}

	_, needs, err := env.svc.Trash(ctx, a.ID, refs, ConfirmInBand, nil)
	require.NoError(t, err)
	_, _, err = env.svc.Trash(ctx, a.ID, refs, ConfirmInBand, &ConfirmAnswer{RequestState: needs.RequestState})
	require.NoError(t, err)

	_, needs, err = env.svc.Trash(ctx, a.ID, refs, ConfirmInBand, nil)
	require.NoError(t, err)
	res, _, err := env.svc.Trash(ctx, a.ID, refs, ConfirmInBand, &ConfirmAnswer{RequestState: needs.RequestState, Accepted: true})
	require.NoError(t, err, "the declined call must not have used the only Trash of the day")
	require.Equal(t, "done", res.Status)
}

// When the backend fails after it moved some or all of the messages, the agent
// learns that the change happened in part, and the attempt counts toward the
// Trash limit.
func TestTrashPartialIsReportedAndCounted(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailDelete), autonomy, func(a *account.Account) { a.Limits.TrashPerDay = 3 })
	env.backend.setErr("Trash", fmt.Errorf("imap: the messages were moved, but the new reference of message 1 is not known: %w", mail.ErrPartial))

	_, _, err := env.svc.Trash(env.ctx(), a.ID, []mail.Ref{"r1", "r2"}, ConfirmInBand, nil)
	require.ErrorIs(t, err, mail.ErrPartial)
	require.Contains(t, err.Error(), "Search again")
	requireEntry(t, env.lastEntry(), "trash_messages", audit.OutcomeError, "partial")
	require.Equal(t, 2, env.lastEntry().Count)

	env.backend.setErr("Trash", nil)
	_, _, err = env.svc.Trash(env.ctx(), a.ID, []mail.Ref{"r3", "r4"}, ConfirmInBand, nil)
	var rate *policy.RateLimitError
	require.ErrorAs(t, err, &rate, "the two messages of the failed call count: 2 + 2 is more than 3")
}

// A partial Trash after an approval ends the action: it must not run again.
func TestApprovedTrashThatFailsInPartEnds(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailDelete))
	env.backend.setErr("Trash", fmt.Errorf("imap: 1 of 2 messages were already moved: %w", mail.ErrPartial))
	_, _, err := env.svc.Trash(env.ctx(), a.ID, []mail.Ref{"r1", "r2"}, ConfirmFallback, nil)
	require.NoError(t, err)
	id := env.waitingActions()[0].ID

	err = env.svc.ApprovePending(env.ctx(), id)
	require.ErrorIs(t, err, mail.ErrPartial)
	require.Equal(t, confirm.StateDone, env.pending(id).State)
}

// ---------------------------------------------------------------- audit failure

// A write that was done stays done when its audit entry cannot be written. The
// failure goes to the log as a code, with no mail data.
func TestAuditFailureDoesNotFailADoneAction(t *testing.T) {
	env := newWriteEnv(t, func(d *Deps) {
		d.Audit = audit.New(failingAuditStore{}, d.Vault, d.Now)
	})
	a := env.account(level(account.MailSend), autonomy, func(a *account.Account) { a.Limits.UndoWindowAuto = 0 })
	ctx := env.ctx()

	require.NoError(t, env.svc.SetFlags(ctx, a.ID, []mail.Ref{"secret-ref"}, mail.FlagChange{Seen: new(true)}))

	c := mail.Compose{To: []string{"bob@example.com"}, Subject: "Quarterly numbers", Body: "Body text"}
	res, _, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, nil)
	require.NoError(t, err)
	require.Equal(t, "sent", res.Status)
	require.Len(t, env.sender.sent(), 1)

	logs := env.logs.String()
	require.Contains(t, logs, "audit_write_failed")
	require.Equal(t, 2, strings.Count(logs, "audit_write_failed"), "one line for each write")
	for _, secret := range []string{"bob@example.com", "me@example.com", "Quarterly", "Body text", "secret-ref", "disk full"} {
		require.NotContains(t, logs, secret)
	}
}

func TestWritesRefusedWithoutAnAuditLog(t *testing.T) {
	env := newWriteEnv(t, func(d *Deps) { d.Audit = nil })
	a := env.account(level(account.MailDelete), autonomy)
	for name, use := range writeUses(env.svc, a.ID) {
		t.Run(name, func(t *testing.T) {
			require.Error(t, use(env.ctx()))
		})
	}
	require.Empty(t, env.backend.trashCalls())
	require.Empty(t, env.backend.moveCalls())
	require.Empty(t, env.sender.sent())
}
