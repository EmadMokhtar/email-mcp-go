package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/confirm"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/outbox"
	"github.com/EmadMokhtar/email-mcp-go/internal/provider/imapsmtp"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
	"github.com/EmadMokhtar/email-mcp-go/internal/signing"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
	"github.com/EmadMokhtar/email-mcp-go/internal/web"
)

// ---------------------------------------------------------------- helpers

// syncBuffer is a bytes.Buffer that many goroutines may write to. It collects
// the log of a test.
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

// fakeBackend is a mail.Backend for tests. A method that a test does not expect
// panics, because the embedded interface is nil.
type fakeBackend struct {
	mail.Backend

	mu         sync.Mutex
	sendErr    error
	discardErr error
	sent       []*mail.RawMessage
	discarded  []mail.Ref
	sendCalls  int
	// onSend runs at the start of each send, with the number of the send
	// (1 for the first). A test uses it to hold a send or to look around.
	onSend func(n int)
}

func (b *fakeBackend) Send(_ context.Context, msg *mail.RawMessage) error {
	b.mu.Lock()
	b.sendCalls++
	n, hook := b.sendCalls, b.onSend
	b.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sent = append(b.sent, msg)
	return b.sendErr
}

// holdFirstSend makes the first send wait until the returned release is
// called. started is closed when the first send has started.
func (b *fakeBackend) holdFirstSend() (started <-chan struct{}, release func()) {
	start := make(chan struct{})
	free := make(chan struct{})
	b.mu.Lock()
	b.onSend = func(n int) {
		if n == 1 {
			close(start)
			<-free
		}
	}
	b.mu.Unlock()
	var once sync.Once
	return start, func() { once.Do(func() { close(free) }) }
}

func (b *fakeBackend) DiscardDraft(_ context.Context, ref mail.Ref) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.discarded = append(b.discarded, ref)
	return b.discardErr
}

func (b *fakeBackend) sentCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.sent)
}

func (b *fakeBackend) discardedRefs() []mail.Ref {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]mail.Ref(nil), b.discarded...)
}

// fakeFactory gives every account the same fake backend. It records the idle
// sweeps and whether it was closed.
type fakeFactory struct {
	backend *fakeBackend

	mu     sync.Mutex
	sweeps []time.Duration
	closed bool
}

func (f *fakeFactory) Mail(context.Context, *account.Account) (mail.Backend, error) {
	return f.backend, nil
}

func (f *fakeFactory) Forget(account.AccountID) {}

func (f *fakeFactory) SweepIdle(maxIdle time.Duration) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sweeps = append(f.sweeps, maxIdle)
	return 0
}

func (f *fakeFactory) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
}

func (f *fakeFactory) sweepCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sweeps)
}

func (f *fakeFactory) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// testApp is an App on a new data directory, with a file master key and a fake
// backend factory.
type testApp struct {
	*App
	dir     string
	keyPath string
	logs    *syncBuffer
	factory *fakeFactory
}

// openTest opens an App for a test. It closes the App when the test ends.
func openTest(t *testing.T) *testApp {
	t.Helper()
	dir := t.TempDir()
	keyPath := filepath.Join(t.TempDir(), "master.key")
	logs := &syncBuffer{}
	factory := &fakeFactory{backend: &fakeBackend{}}
	a, err := open(context.Background(), Config{
		DataDir:   dir,
		KEKSource: "file:" + keyPath,
		Local:     true,
		LogOutput: logs,
	}, options{backends: factory})
	require.NoError(t, err)
	t.Cleanup(func() { _ = a.Close(context.Background()) })
	return &testApp{App: a, dir: dir, keyPath: keyPath, logs: logs, factory: factory}
}

// addAccount stores an account of the local tenant. The fake factory never
// connects, so the settings do not matter.
func (ta *testApp) addAccount(t *testing.T, change ...func(*account.Account)) *account.Account {
	t.Helper()
	a := account.NewIMAPAccount(store.LocalTenantID, "me@example.com", account.MailServer{
		IMAPHost: "imap.example.com", IMAPPort: 993, IMAPSecurity: account.SecurityTLS,
		SMTPHost: "smtp.example.com", SMTPPort: 465, SMTPSecurity: account.SecurityTLS,
		Username: "me@example.com",
	}, time.Now())
	for _, fn := range change {
		fn(a)
	}
	require.NoError(t, ta.store.CreateAccount(context.Background(), a))
	return a
}

// testMessage is a message to queue.
func testMessage() *mail.RawMessage {
	return &mail.RawMessage{
		From:       "me@example.com",
		Recipients: []string{"you@example.com"},
		Data:       []byte("Subject: hello\r\n\r\nhello\r\n"),
	}
}

// localCtx returns a context with the local principal.
func localCtx() context.Context {
	return service.WithPrincipal(context.Background(), service.Principal{Tenant: store.LocalTenantID, Client: "test"})
}

// outboxStatus reads the status of an item from a new handle on the database,
// so it works after the App closed its store.
func outboxStatus(t *testing.T, dir, id string) (status, errText string) {
	t.Helper()
	st, err := store.Open(context.Background(), dir)
	require.NoError(t, err)
	defer func() { require.NoError(t, st.Close()) }()
	row, err := st.GetOutbox(context.Background(), store.LocalTenantID, id)
	require.NoError(t, err)
	return row.Status, row.Error
}

// ---------------------------------------------------------------- Open

func TestOpenWiresEverything(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	keyPath := filepath.Join(t.TempDir(), "master.key")
	logs := &syncBuffer{}

	a, err := Open(context.Background(), Config{DataDir: dir, KEKSource: "file:" + keyPath, Local: true, LogOutput: logs})
	require.NoError(t, err)
	t.Cleanup(func() { _ = a.Close(context.Background()) })

	require.NotNil(t, a.Service)
	require.NotNil(t, a.Outbox)
	require.NotNil(t, a.Web)
	require.NotNil(t, a.Tokens)
	views, err := a.Service.ListAccounts(localCtx())
	require.NoError(t, err)
	require.Empty(t, views)

	// The data directory is private, and the new master key file too.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(dir)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
		info, err = os.Stat(keyPath)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}

	// The connect link points at the local web server and has no token.
	link, err := a.Service.ConnectLink(localCtx())
	require.NoError(t, err)
	require.Regexp(t, `^http://127\.0\.0\.1:\d+/connect$`, link)
	require.NotContains(t, logs.String(), link, "the log never has a link")
}

// The log of the App writes Info and higher by default. Config.LogLevel lowers
// the level, so a test can see every line of every part, Debug lines too.
func TestOpenLogLevel(t *testing.T) {
	for name, tc := range map[string]struct {
		level     slog.Leveler
		wantDebug bool
	}{
		"default is Info": {level: nil, wantDebug: false},
		"Debug":           {level: slog.LevelDebug, wantDebug: true},
	} {
		t.Run(name, func(t *testing.T) {
			logs := &syncBuffer{}
			a, err := Open(context.Background(), Config{
				DataDir: t.TempDir(), KEKSource: "file:" + filepath.Join(t.TempDir(), "master.key"),
				LogOutput: logs, LogLevel: tc.level,
			})
			require.NoError(t, err)
			t.Cleanup(func() { _ = a.CloseNow(context.Background()) })

			a.Logger().Debug("debug line of the test")
			slog.Debug("debug line of the default logger")
			a.Logger().Info("info line of the test")

			require.Contains(t, logs.String(), "info line of the test")
			require.Equal(t, tc.wantDebug, strings.Contains(logs.String(), "debug line of the test"))
			require.Equal(t, tc.wantDebug, strings.Contains(logs.String(), "debug line of the default logger"),
				"the default logger of the program uses the same level")
		})
	}
}

// Config.Now is the clock of every part that Open builds. A test that must let
// a request state expire moves this clock instead of waiting 10 minutes.
func TestOpenUsesTheClockOfTheConfig(t *testing.T) {
	at := time.Date(2030, 5, 6, 7, 8, 9, 0, time.UTC)
	a, err := Open(context.Background(), Config{
		DataDir: t.TempDir(), KEKSource: "file:" + filepath.Join(t.TempDir(), "master.key"),
		LogOutput: &syncBuffer{}, Now: func() time.Time { return at },
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = a.CloseNow(context.Background()) })

	// The tokens and the audit log of the service.
	_, err = a.Service.CreateToken(localCtx(), "clock", []string{"email:read"})
	require.NoError(t, err)
	list, err := a.Tokens.List(context.Background(), store.LocalTenantID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.True(t, at.Equal(list[0].CreatedAt), "the token was made at the time of the clock")
	entries, err := a.audit.List(context.Background(), store.LocalTenantID, audit.Filter{})
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	require.True(t, at.Equal(entries[0].Time), "the audit entry has the time of the clock")
	// The other parts keep the clock in private fields. The e2e test of the
	// confirmations checks that a request state expires on this clock.
}

func TestConfigDefaults(t *testing.T) {
	t.Run("explicit values win", func(t *testing.T) {
		t.Setenv(envDataDir, "/from/env")
		t.Setenv(envKEK, "file:/env.key")
		got, err := Config{DataDir: "/explicit", KEKSource: "keychain"}.resolve()
		require.NoError(t, err)
		require.Equal(t, "/explicit", got.DataDir)
		require.Equal(t, "keychain", got.KEKSource)
	})
	t.Run("environment", func(t *testing.T) {
		t.Setenv(envDataDir, "/from/env")
		t.Setenv(envKEK, "file:/env.key")
		got, err := Config{}.resolve()
		require.NoError(t, err)
		require.Equal(t, "/from/env", got.DataDir)
		require.Equal(t, "file:/env.key", got.KEKSource)
	})
	t.Run("platform defaults", func(t *testing.T) {
		t.Setenv(envDataDir, "")
		t.Setenv(envKEK, "")
		t.Setenv("XDG_DATA_HOME", "/xdg/data")
		got, err := Config{}.resolve()
		require.NoError(t, err)
		require.Equal(t, "keychain", got.KEKSource)
		switch runtime.GOOS {
		case "darwin", "windows":
			base, err := os.UserConfigDir()
			require.NoError(t, err)
			require.Equal(t, filepath.Join(base, "email-mcp"), got.DataDir)
		default:
			require.Equal(t, filepath.Join("/xdg/data", "email-mcp"), got.DataDir)
		}
	})
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Run("XDG_DATA_HOME not set", func(t *testing.T) {
			t.Setenv(envDataDir, "")
			t.Setenv("XDG_DATA_HOME", "")
			home, err := os.UserHomeDir()
			require.NoError(t, err)
			got, err := Config{}.resolve()
			require.NoError(t, err)
			require.Equal(t, filepath.Join(home, ".local", "share", "email-mcp"), got.DataDir)
		})
	}
}

func TestOpenRefusesAnUnknownKEKSource(t *testing.T) {
	for _, source := range []string{"vault", "file:", "keychain:x", "FILE:/a", "file:relative/master.key"} {
		t.Run(source, func(t *testing.T) {
			_, err := Open(context.Background(), Config{DataDir: t.TempDir(), KEKSource: source, LogOutput: &syncBuffer{}})
			require.Error(t, err)
		})
	}
}

// A relative key file path would depend on the directory that the MCP client
// starts the server in, so it is refused before anything is made.
func TestOpenRefusesARelativeKeyFilePath(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)

	_, err := Open(context.Background(), Config{DataDir: t.TempDir(), KEKSource: "file:master.key", LogOutput: &syncBuffer{}})

	require.ErrorContains(t, err, "absolute")
	_, statErr := os.Stat(filepath.Join(cwd, "master.key"))
	require.ErrorIs(t, statErr, os.ErrNotExist, "no key file is made")
}

// setUserDirs points the user directories of the platform (os.UserConfigDir)
// at home.
func setUserDirs(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("AppData", home)
}

// The keychain entry belongs to the user, not to a data directory. So the
// first-run lock of the keychain is in a directory of the user, and two data
// directories cannot each make a keychain key. A key file stays with its data
// directory.
func TestKeychainLockIsPerUser(t *testing.T) {
	setUserDirs(t, t.TempDir())
	base, err := os.UserConfigDir()
	require.NoError(t, err)
	dataDir := t.TempDir()

	dir, err := kekLockDir(kekKeychain, dataDir)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(base, "email-mcp"), dir)
	if runtime.GOOS != "windows" {
		info, err := os.Stat(dir)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	}

	dir, err = kekLockDir("file:/keys/master.key", dataDir)
	require.NoError(t, err)
	require.Equal(t, dataDir, dir)
}

// Open with the keychain waits for the lock of the user, also when its data
// directory is new and has no lock of its own. The keychain is the in-memory
// mock of go-keyring, so the test never touches the real keychain.
func TestOpenWithTheKeychainWaitsForTheLockOfTheUser(t *testing.T) {
	keyring.MockInit()
	setUserDirs(t, t.TempDir())
	base, err := os.UserConfigDir()
	require.NoError(t, err)
	userDir := filepath.Join(base, "email-mcp")
	require.NoError(t, os.MkdirAll(userDir, 0o700))
	unlock, err := lockDir(context.Background(), userDir)
	require.NoError(t, err)

	done := make(chan error, 1)
	var a *App
	go func() {
		var err error
		a, err = Open(context.Background(), Config{DataDir: t.TempDir(), KEKSource: kekKeychain, LogOutput: &syncBuffer{}})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("Open finished while another program held the keychain lock of the user: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	unlock()
	require.NoError(t, <-done)
	require.NoError(t, a.Close(context.Background()))
}

// Two programs that start at the same time on a new data directory must end
// with one master key. Without the lock, both could find no key file and make
// one each, or one could read the file while the other still writes it. The
// hook makes the time between the look and the make long, so that every Open
// would look before the first one makes the key, if the lock let them.
func TestConcurrentOpensCreateOneKey(t *testing.T) {
	afterKeyLookup = func() { time.Sleep(50 * time.Millisecond) }
	t.Cleanup(func() { afterKeyLookup = func() {} })
	dir := t.TempDir()
	keyPath := filepath.Join(t.TempDir(), "master.key")
	cfg := Config{DataDir: dir, KEKSource: "file:" + keyPath, Local: true, LogOutput: &syncBuffer{}}

	const n = 4
	apps := make([]*App, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			apps[i], errs[i] = Open(context.Background(), cfg)
		}()
	}
	wg.Wait()
	for i := range n {
		require.NoError(t, errs[i], "open %d", i)
		t.Cleanup(func() { _ = apps[i].Close(context.Background()) })
	}

	first, err := apps[0].vault.ServerKey(context.Background())
	require.NoError(t, err)
	for i := 1; i < n; i++ {
		key, err := apps[i].vault.ServerKey(context.Background())
		require.NoError(t, err)
		require.Equal(t, first, key, "every program unwraps the same server key with the same master key")
	}
}

// Open makes the master key under a lock in the data directory, so it waits for
// another program that holds the lock.
func TestOpenWaitsForTheKeyLock(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(t.TempDir(), "master.key")
	unlock, err := lockDir(context.Background(), dir)
	require.NoError(t, err)

	done := make(chan error, 1)
	var a *App
	go func() {
		var err error
		a, err = Open(context.Background(), Config{DataDir: dir, KEKSource: "file:" + keyPath, LogOutput: &syncBuffer{}})
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("Open finished while another program held the lock: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	_, statErr := os.Stat(keyPath)
	require.ErrorIs(t, statErr, os.ErrNotExist, "no key is made while another program holds the lock")

	unlock()
	require.NoError(t, <-done)
	t.Cleanup(func() { _ = a.Close(context.Background()) })
	_, err = os.Stat(keyPath)
	require.NoError(t, err)
}

// A missing key file is made only for a database that has no keys yet. For a
// database with keys, a new key could never open them: the path is wrong.
func TestOpenRefusesToMakeAKeyFileForADatabaseWithKeys(t *testing.T) {
	dir := t.TempDir()
	keys := t.TempDir()
	first, err := Open(context.Background(), Config{DataDir: dir, KEKSource: "file:" + filepath.Join(keys, "master.key"), LogOutput: &syncBuffer{}})
	require.NoError(t, err)
	require.NoError(t, first.Close(context.Background()))

	wrong := filepath.Join(keys, "typo.key")
	_, err = Open(context.Background(), Config{DataDir: dir, KEKSource: "file:" + wrong, LogOutput: &syncBuffer{}})
	require.Error(t, err)
	require.ErrorContains(t, err, "does not exist")
	_, statErr := os.Stat(wrong)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

// The keychain gets the same guard as a key file: a database that holds
// wrapped keys needs the key that wrapped them. When the keychain entry is gone
// (a data directory restored without it), Open refuses, and it makes no new
// entry: a new random key could never open the keys, and it would block a later
// restore of the real one. The keychain is the in-memory mock of go-keyring.
func TestOpenRefusesToMakeAKeychainKeyForADatabaseWithKeys(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(keyring.MockInit)
	setUserDirs(t, t.TempDir())
	dir := t.TempDir()
	first, err := Open(context.Background(), Config{DataDir: dir, KEKSource: kekKeychain, LogOutput: &syncBuffer{}})
	require.NoError(t, err)
	require.NoError(t, first.Close(context.Background()))
	require.NoError(t, keyring.Delete("email-mcp", "master-key"))

	_, err = Open(context.Background(), Config{DataDir: dir, KEKSource: kekKeychain, LogOutput: &syncBuffer{}})

	require.ErrorContains(t, err, "the keychain has no master key")
	_, getErr := keyring.Get("email-mcp", "master-key")
	require.ErrorIs(t, getErr, keyring.ErrNotFound, "no new keychain key was made")
}

// A "..": part in a key file path is refused. The writer of a new key file
// resolves ".." after a symbolic link on the disk, while the reader cleans it as
// text, so the two could use different files.
func TestCheckKEKSourceRefusesDotDot(t *testing.T) {
	for _, source := range []string{"file:/keys/../master.key", "file:/keys/sub/..", "file:/../master.key"} {
		err := CheckKEKSource(source)
		require.Error(t, err, source)
		require.NotContains(t, err.Error(), "keys", "the text is fixed and names no path")
	}
	for _, source := range []string{"file:/keys/..master.key", "file:/keys/master..key", "file:/keys/.hidden/master.key"} {
		require.NoError(t, CheckKEKSource(source), source)
	}
}

// ---------------------------------------------------------------- Sender

func TestSenderMarksUnknownDeliveryAndHardRefusalsPermanent(t *testing.T) {
	for name, tc := range map[string]struct {
		err       error
		permanent bool
	}{
		"success":                 {err: nil},
		"delivery unknown":        {err: fmt.Errorf("%w: end of data: EOF", imapsmtp.ErrDeliveryUnknown), permanent: true},
		"recipient refused (5xx)": {err: &imapsmtp.RecipientRejectedError{Index: 0, Total: 1, Code: 550}, permanent: true},
		"recipient refused (4xx)": {err: &imapsmtp.RecipientRejectedError{Index: 0, Total: 1, Code: 450}},
		"time limit":              {err: fmt.Errorf("smtp: %w", context.DeadlineExceeded), permanent: true},
		"cancelled":               {err: fmt.Errorf("smtp: %w", context.Canceled), permanent: true},
		"connection refused":      {err: errors.New("smtp: dial: connection refused")},
		"message cannot be sent":  {err: fmt.Errorf("%w: recipient 1 of 1 is not a valid address", imapsmtp.ErrInvalidMessage), permanent: true},
	} {
		t.Run(name, func(t *testing.T) {
			ta := openTest(t)
			acct := ta.addAccount(t)
			ta.factory.backend.sendErr = tc.err

			err := ta.sender(context.Background(), store.LocalTenantID, acct.ID, testMessage(), "draft-ref")

			if tc.err == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tc.err, "the cause stays visible")
			require.Equal(t, tc.permanent, outbox.IsPermanent(err))
			require.Empty(t, ta.factory.backend.discardedRefs(), "a draft is discarded only after a send")
		})
	}
}

func TestSenderDiscardsTheDraftAfterTheSend(t *testing.T) {
	ta := openTest(t)
	acct := ta.addAccount(t)

	require.NoError(t, ta.sender(context.Background(), store.LocalTenantID, acct.ID, testMessage(), "draft-ref-1"))
	require.Equal(t, []mail.Ref{"draft-ref-1"}, ta.factory.backend.discardedRefs())

	// Without a draft, nothing is discarded.
	require.NoError(t, ta.sender(context.Background(), store.LocalTenantID, acct.ID, testMessage(), ""))
	require.Len(t, ta.factory.backend.discardedRefs(), 1)
}

// A failed discard does not change the result: the message was sent, and an
// error would make the outbox send it again.
func TestSenderDiscardErrorDoesNotChangeTheResult(t *testing.T) {
	for name, discardErr := range map[string]error{
		"not a draft": fmt.Errorf("imap: %w", mail.ErrUnsupported),
		"other":       errors.New("imap: the server said NO to secret-subject"),
	} {
		t.Run(name, func(t *testing.T) {
			ta := openTest(t)
			acct := ta.addAccount(t)
			ta.factory.backend.discardErr = discardErr

			err := ta.sender(context.Background(), store.LocalTenantID, acct.ID, testMessage(), "draft-ref-secret")

			require.NoError(t, err)
			logs := ta.logs.String()
			require.Contains(t, logs, "draft_discard_failed")
			require.NotContains(t, logs, "draft-ref-secret", "the log never has the ref")
			require.NotContains(t, logs, "secret-subject", "the log never has the text of the error")
		})
	}
}

func TestSenderRefusesAnAccountThatIsGone(t *testing.T) {
	ta := openTest(t)

	err := ta.sender(context.Background(), store.LocalTenantID, "missing", testMessage(), "")

	require.Error(t, err)
	require.True(t, outbox.IsPermanent(err), "a retry cannot bring the account back")
	require.Zero(t, ta.factory.backend.sentCount())
}

// The service never calls the mail server for an account that must be
// reconnected, and neither does the outbox. The error is not permanent: the user
// can reconnect the account before the next try.
func TestSenderDoesNotCallTheServerForAnAccountThatNeedsReauth(t *testing.T) {
	ta := openTest(t)
	acct := ta.addAccount(t, func(a *account.Account) { a.Status = account.StatusNeedsReauth })

	err := ta.sender(context.Background(), store.LocalTenantID, acct.ID, testMessage(), "")

	require.Error(t, err)
	require.False(t, outbox.IsPermanent(err))
	require.Zero(t, ta.factory.backend.sentCount())
}

// When the SMTP server refuses the stored login, the outbox marks the account
// for a reconnect, as the service does for its own calls. The error is not
// permanent: the user may fix the account (email-mcp account test) before the
// next try. A busy server does not mark the account.
func TestSenderMarksTheAccountWhenTheServerRefusesTheLogin(t *testing.T) {
	ta := openTest(t)
	acct := ta.addAccount(t)
	ta.factory.backend.sendErr = fmt.Errorf("smtp: login failed (reply code 535): %w", imapsmtp.ErrAuthFailed)

	err := ta.sender(context.Background(), store.LocalTenantID, acct.ID, testMessage(), "")

	require.ErrorIs(t, err, imapsmtp.ErrAuthFailed)
	require.False(t, outbox.IsPermanent(err))
	got, err := ta.store.GetAccount(context.Background(), store.LocalTenantID, acct.ID)
	require.NoError(t, err)
	require.Equal(t, account.StatusNeedsReauth, got.Status)
	require.Contains(t, ta.logs.String(), "code=needs_reauth_set")

	other := ta.addAccount(t, func(a *account.Account) { a.Address = "other@example.com" })
	ta.factory.backend.sendErr = fmt.Errorf("imap: login failed [INUSE]: %w", imapsmtp.ErrServerBusy)
	_ = ta.sender(context.Background(), store.LocalTenantID, other.ID, testMessage(), "")
	got, err = ta.store.GetAccount(context.Background(), store.LocalTenantID, other.ID)
	require.NoError(t, err)
	require.Equal(t, account.StatusOK, got.Status, "a busy server did not refuse the password")
}

// ---------------------------------------------------------------- Run

// A program that stopped in the middle of a send left its item in "sending".
// The server may have the message, so the item ends "failed" and is never sent
// again. This happens at start-up, before the worker runs.
func TestRunFailsInterruptedSendsBeforeTheWorkerStarts(t *testing.T) {
	ta := openTest(t)
	acct := ta.addAccount(t)
	claimed := time.Now().Add(-20 * time.Minute).Truncate(time.Millisecond)
	row := &store.OutboxRow{
		ID: "interrupted", Tenant: store.LocalTenantID, Account: acct.ID, Status: "sending",
		SendAfter: claimed, NextAttemptAt: claimed, CreatedAt: claimed, ClaimedAt: claimed,
	}
	require.NoError(t, ta.store.CreateOutboxSending(context.Background(), row, []byte("sealed"), 1, claimed))

	ta.Run(context.Background())

	// No wait: the recovery is done when Run returns.
	item, err := ta.Outbox.Get(context.Background(), store.LocalTenantID, "interrupted")
	require.NoError(t, err)
	require.Equal(t, outbox.StatusFailed, item.Status)
	require.Contains(t, item.Error, "delivery unknown")

	require.NoError(t, ta.Close(context.Background()))
	require.Zero(t, ta.factory.backend.sentCount(), "an interrupted send is never sent again")
}

// An approved action with no result whose approval time ended long ago is left
// over from a program that stopped after the approval. Run expires it at
// start-up. An approval that a live program can still finish is kept.
func TestRunExpiresStaleApprovedActions(t *testing.T) {
	ta := openTest(t)
	acct := ta.addAccount(t)
	ctx := context.Background()
	signer := signing.New(bytes.Repeat([]byte{9}, 32))

	createApproved := func(at time.Time, payload string) *confirm.Pending {
		m := confirm.NewManager(ta.store, ta.vault, signer, func() time.Time { return at })
		p, err := m.Create(ctx, store.LocalTenantID, acct.ID, confirm.KindSend, confirm.ModeInBand, []byte(payload),
			confirm.Preview{Account: "me", Count: 1})
		require.NoError(t, err)
		require.NoError(t, m.Transition(ctx, store.LocalTenantID, p.ID, confirm.StatePending, confirm.StateApproved, ""))
		return p
	}
	stale := createApproved(time.Now().Add(-time.Hour), "stale")
	live := createApproved(time.Now().Add(-time.Minute), "live")

	ta.Run(ctx)

	got, _, err := ta.confirm.Get(ctx, store.LocalTenantID, stale.ID)
	require.NoError(t, err)
	require.Equal(t, confirm.StateExpired, got.State)
	got, _, err = ta.confirm.Get(ctx, store.LocalTenantID, live.ID)
	require.NoError(t, err)
	require.Equal(t, confirm.StateApproved, got.State, "a send may still be running for it")
}

// The daily jobs prune old pending actions and old audit entries, expire stale
// approvals, and cut the write-ahead log. They run at start and then at every
// interval.
func TestDailyJobsPruneAndCheckpoint(t *testing.T) {
	ta := openTest(t)
	ta.jobEvery = 20 * time.Millisecond
	acct := ta.addAccount(t)
	ctx := context.Background()
	signer := signing.New(bytes.Repeat([]byte{9}, 32))

	ta.Run(ctx)

	// Rows that are older than the limits appear after the first run.
	old := time.Now().Add(-100 * 24 * time.Hour)
	oldClock := func() time.Time { return old }
	m := confirm.NewManager(ta.store, ta.vault, signer, oldClock)
	waited, err := m.Create(ctx, store.LocalTenantID, acct.ID, confirm.KindSend, confirm.ModeInBand, []byte("old"),
		confirm.Preview{Account: "me", Count: 1})
	require.NoError(t, err)
	require.NoError(t, audit.New(ta.store, ta.vault, oldClock).Record(ctx, audit.Entry{
		Tenant: store.LocalTenantID, Account: acct.ID, Area: account.AreaMail, Tool: "send_message",
		Count: 1, Recipients: []string{"you@example.com"}, Outcome: audit.OutcomeOK,
	}))
	recent := audit.Entry{Tenant: store.LocalTenantID, Tool: "list_accounts", Outcome: audit.OutcomeOK}
	require.NoError(t, audit.New(ta.store, ta.vault, nil).Record(ctx, recent))

	walPath := filepath.Join(ta.dir, "email-mcp.db-wal")
	require.Eventually(t, func() bool {
		_, _, err := ta.confirm.Get(ctx, store.LocalTenantID, waited.ID)
		entries, listErr := ta.audit.List(ctx, store.LocalTenantID, audit.Filter{Limit: 10})
		info, statErr := os.Stat(walPath)
		return errors.Is(err, confirm.ErrNotFound) && listErr == nil && len(entries) == 1 &&
			statErr == nil && info.Size() == 0
	}, 5*time.Second, 10*time.Millisecond, "the old rows are pruned and the log is cut")

	entries, err := ta.audit.List(ctx, store.LocalTenantID, audit.Filter{Limit: 10})
	require.NoError(t, err)
	require.Equal(t, "list_accounts", entries[0].Tool, "a recent entry stays")
	require.Contains(t, ta.logs.String(), "daily jobs done")
	require.Contains(t, ta.logs.String(), "checkpoint=truncate")
}

// At start-up the jobs use a passive checkpoint: a TRUNCATE checkpoint waits for
// the other programs that use the database, and holds up their writes, for up
// to the busy timeout. Every program runs the jobs when it starts.
func TestRunStartUsesAPassiveCheckpoint(t *testing.T) {
	ta := openTest(t)
	ta.addAccount(t) // a write, so the log is not empty

	ta.Run(context.Background())

	require.Eventually(t, func() bool { return strings.Contains(ta.logs.String(), "daily jobs done") },
		5*time.Second, 5*time.Millisecond)
	require.Contains(t, ta.logs.String(), "checkpoint=passive")
	info, err := os.Stat(filepath.Join(ta.dir, "email-mcp.db-wal"))
	require.NoError(t, err)
	require.Positive(t, info.Size(), "the log file is not cut at start-up")
}

// The backend cache is swept now and then, so the backend of an account that
// another process deleted does not stay in memory.
func TestRunSweepsIdleBackends(t *testing.T) {
	ta := openTest(t)
	ta.sweepEvery = 10 * time.Millisecond

	ta.Run(context.Background())

	require.Eventually(t, func() bool { return ta.factory.sweepCount() >= 2 }, 5*time.Second, 5*time.Millisecond)
	ta.factory.mu.Lock()
	defer ta.factory.mu.Unlock()
	require.Equal(t, 30*time.Minute, ta.factory.sweeps[0])
}

// ---------------------------------------------------------------- Close

func TestCloseDrainsOutbox(t *testing.T) {
	ta := openTest(t)
	acct := ta.addAccount(t)

	item, err := ta.Outbox.Enqueue(context.Background(), store.LocalTenantID, acct.ID, testMessage(), time.Second, "")
	require.NoError(t, err)
	require.Equal(t, outbox.StatusQueued, item.Status)

	require.NoError(t, ta.Close(context.Background()))

	require.Equal(t, 1, ta.factory.backend.sentCount())
	status, _ := outboxStatus(t, ta.dir, item.ID)
	require.Equal(t, "sent", status)
	require.True(t, ta.factory.isClosed(), "the backends are closed")
}

// Close stops the worker and waits for the send that it runs. Only then does it
// drain the queue and close the store, so the result of the send is written.
func TestCloseWaitsForTheSendOfTheWorker(t *testing.T) {
	ta := openTest(t)
	acct := ta.addAccount(t)
	started, release := ta.factory.backend.holdFirstSend()
	defer release()

	item, err := ta.Outbox.Enqueue(context.Background(), store.LocalTenantID, acct.ID, testMessage(), time.Millisecond, "")
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond) // the item is due, so the first pass of the worker takes it
	ta.Run(context.Background())
	<-started

	closed := make(chan error, 1)
	go func() { closed <- ta.Close(context.Background()) }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned while a send was running: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	// Close waits for the worker, and already takes no new tool call.
	call := ta.trackToolCalls(func(context.Context, string, mcp.Request) (mcp.Result, error) {
		t.Error("a tool call ran while the App stopped")
		return nil, nil
	})
	_, err = call(context.Background(), "tools/call", nil)
	require.ErrorIs(t, err, errStopping)

	release()
	require.NoError(t, <-closed)
	status, _ := outboxStatus(t, ta.dir, item.ID)
	require.Equal(t, "sent", status, "the result of the running send is written before the store closes")
	require.Equal(t, 1, ta.factory.backend.sentCount())
}

// Close waits for the tool calls that run (a send without an undo window runs
// inside its tool call), and refuses new ones.
func TestCloseWaitsForRunningToolCalls(t *testing.T) {
	ta := openTest(t)
	release := make(chan struct{})
	started := make(chan struct{})
	handler := ta.trackToolCalls(func(context.Context, string, mcp.Request) (mcp.Result, error) {
		close(started)
		<-release
		return &mcp.CallToolResult{}, nil
	})

	callDone := make(chan error, 1)
	go func() {
		_, err := handler(context.Background(), "tools/call", nil)
		callDone <- err
	}()
	<-started

	closed := make(chan error, 1)
	go func() { closed <- ta.Close(context.Background()) }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned while a tool call was running: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	_, err := handler(context.Background(), "tools/call", nil)
	require.ErrorIs(t, err, errStopping, "a new tool call is refused while the program stops")
	// Other methods, such as tools/list, are not counted.
	other := ta.trackToolCalls(func(context.Context, string, mcp.Request) (mcp.Result, error) { return nil, nil })
	_, err = other(context.Background(), "tools/list", nil)
	require.NoError(t, err)

	close(release)
	require.NoError(t, <-callDone)
	require.NoError(t, <-closed)
}

// A send without an undo window runs inside the service call that asked for it
// (a tool call, or an approval on the web page). Close must wait for that
// service action before it closes the backends and the store, or the result of
// a message that went out would never be written. This holds the send inside
// Service.Send itself, with no tool call around it, so only the wait for the
// service actions keeps Close from going on.
func TestCloseWaitsForAnInlineSendOfTheService(t *testing.T) {
	ta := openTest(t)
	acct := ta.addAccount(t, func(a *account.Account) {
		a.ConfirmSends = false
		a.Limits.UndoWindowAuto = 0
	})
	started, release := ta.factory.backend.holdFirstSend()
	defer release()

	type sendResult struct {
		res *service.SendResult
		err error
	}
	sent := make(chan sendResult, 1)
	go func() {
		c := mail.Compose{To: []string{"bob@example.com"}, Subject: "Plans", Body: "See you at noon."}
		res, _, err := ta.Service.Send(localCtx(), acct.ID, &c, nil, service.ConfirmFallback, nil)
		sent <- sendResult{res, err}
	}()
	<-started

	closed := make(chan error, 1)
	go func() { closed <- ta.Close(context.Background()) }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned while a send of the service was running: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	release()
	got := <-sent
	require.NoError(t, got.err)
	require.Equal(t, service.SendSent, got.res.Status)
	require.NoError(t, <-closed)
	status, _ := outboxStatus(t, ta.dir, got.res.SendID)
	require.Equal(t, "sent", status, "the result is written before the store closes")
}

func TestCloseShutsDownTheWebServer(t *testing.T) {
	ta := openTest(t)
	base, err := ta.Web.EnsureStarted(context.Background())
	require.NoError(t, err)
	addr := strings.TrimPrefix(base, "http://")
	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	require.NoError(t, conn.Close())

	require.NoError(t, ta.Close(context.Background()))

	_, err = ta.Web.EnsureStarted(context.Background())
	require.ErrorIs(t, err, web.ErrClosed)
	_, err = net.DialTimeout("tcp", addr, time.Second)
	require.Error(t, err, "the web server no longer listens")
}

func TestCloseTwice(t *testing.T) {
	ta := openTest(t)
	ta.Run(context.Background())

	require.NoError(t, ta.Close(context.Background()))
	require.NoError(t, ta.Close(context.Background()))

	// Run after Close does nothing.
	ta.Run(context.Background())
}

// A send that waits for approval cannot be approved while the App stops: the
// approval would send the message inside the web request, and the store and the
// backends close soon. The action keeps waiting, and the queued message whose
// undo window ends in the drain window is still sent.
func TestCloseRefusesNewApprovalsWhileItDrains(t *testing.T) {
	ta := openTest(t)
	acct := ta.addAccount(t) // a new account confirms sends
	ctx := localCtx()
	c := mail.Compose{To: []string{"bob@example.com"}, Subject: "Plans", Body: "See you at noon."}
	waiting, _, err := ta.Service.Send(ctx, acct.ID, &c, nil, service.ConfirmFallback, nil)
	require.NoError(t, err)
	require.Equal(t, "pending_approval", waiting.Status)
	queued, err := ta.Outbox.Enqueue(ctx, store.LocalTenantID, acct.ID, testMessage(), 200*time.Millisecond, "")
	require.NoError(t, err)
	inDrain, release := ta.factory.backend.holdFirstSend()
	defer release()

	closed := make(chan error, 1)
	go func() { closed <- ta.Close(context.Background()) }()
	<-inDrain // Close is in its drain and sends the queued message now

	err = ta.Service.ApprovePending(ctx, waiting.SendID)

	require.ErrorIs(t, err, service.ErrStopping)
	p, _, err := ta.confirm.Get(ctx, store.LocalTenantID, waiting.SendID)
	require.NoError(t, err)
	require.Equal(t, confirm.StatePending, p.State, "the action still waits")
	release()
	require.NoError(t, <-closed)
	require.Equal(t, 1, ta.factory.backend.sentCount(), "only the queued message was sent")
	status, _ := outboxStatus(t, ta.dir, queued.ID)
	require.Equal(t, "sent", status)
}

// Close waits for a tool call that runs (step 2), then drains the outbox (step
// 3), which sends what that call queued, and only then shuts down the web
// server (step 4), which stays up during the drain so the user can still reject
// an action that waits for approval.
func TestCloseSendsWhatARunningToolCallQueued(t *testing.T) {
	ta := openTest(t)
	acct := ta.addAccount(t)
	base, err := ta.Web.EnsureStarted(context.Background())
	require.NoError(t, err)
	addr := strings.TrimPrefix(base, "http://")
	var webUpWhileSending atomic.Bool
	ta.factory.backend.onSend = func(int) {
		if conn, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			_ = conn.Close()
			webUpWhileSending.Store(true)
		}
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var queuedID string
	call := ta.trackToolCalls(func(context.Context, string, mcp.Request) (mcp.Result, error) {
		close(started)
		<-release
		item, err := ta.Outbox.Enqueue(context.Background(), store.LocalTenantID, acct.ID, testMessage(), 200*time.Millisecond, "")
		if err != nil {
			return nil, err
		}
		queuedID = item.ID
		return &mcp.CallToolResult{}, nil
	})
	callDone := make(chan error, 1)
	go func() {
		_, err := call(context.Background(), "tools/call", nil)
		callDone <- err
	}()
	<-started

	closed := make(chan error, 1)
	go func() { closed <- ta.Close(context.Background()) }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned while a tool call was running: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)

	require.NoError(t, <-callDone)
	require.NoError(t, <-closed)
	require.Equal(t, 1, ta.factory.backend.sentCount(), "the drain sent what the tool call queued")
	status, _ := outboxStatus(t, ta.dir, queuedID)
	require.Equal(t, "sent", status)
	require.True(t, webUpWhileSending.Load(), "the web server was still up during the drain")
	_, err = ta.Web.EnsureStarted(context.Background())
	require.ErrorIs(t, err, web.ErrClosed, "and it is shut down after the drain")
}

// The stop has one time budget, the deadline of the context of Close. The drain
// starts no send that could not end before it (a send may take
// outbox.SendDeadline). When the time is over, the queued messages stay queued
// for the next start, and that is a clean stop, not an error.
func TestCloseTreatsTheEndOfTheStopTimeAsACleanStop(t *testing.T) {
	ta := openTest(t)
	acct := ta.addAccount(t)
	item, err := ta.Outbox.Enqueue(context.Background(), store.LocalTenantID, acct.ID, testMessage(), 2*time.Second, "")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), outbox.SendDeadline+300*time.Millisecond)
	defer cancel()

	require.NoError(t, ta.Close(ctx))

	require.Zero(t, ta.factory.backend.sentCount())
	status, _ := outboxStatus(t, ta.dir, item.ID)
	require.Equal(t, "queued", status, "the message is sent at the next start")
	require.Contains(t, ta.logs.String(), "drain_stopped")
}
