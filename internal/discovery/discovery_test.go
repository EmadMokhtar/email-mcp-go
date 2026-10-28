package discovery

import (
	"context"
	"errors"
	"fmt"
	"io"
	stdlog "log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/stretchr/testify/require"
)

// testAddress has a local part that is easy to find in a log of requests.
const testAddress = "zebra.crossing@example.com"

// --- helpers: Thunderbird config XML ---

func serverXML(tag, typ, host string, port int, socket, username string) string {
	s := fmt.Sprintf(`<%s type=%q><hostname>%s</hostname><port>%d</port><socketType>%s</socketType>`,
		tag, typ, host, port, socket)
	if username != "" {
		s += "<username>" + username + "</username>"
	}
	return s + "</" + tag + ">"
}

func imapXML(host string, port int, socket, username string) string {
	return serverXML("incomingServer", "imap", host, port, socket, username)
}

func smtpXML(host string, port int, socket, username string) string {
	return serverXML("outgoingServer", "smtp", host, port, socket, username)
}

func configXML(parts ...string) string {
	return `<?xml version="1.0" encoding="UTF-8"?><clientConfig version="1.1">` +
		`<emailProvider id="example.com">` + strings.Join(parts, "") +
		`</emailProvider></clientConfig>`
}

// goodConfig has one IMAP server (993, SSL) and one SMTP server (587,
// STARTTLS). Both use the email address as the user name.
func goodConfig(imapHost, smtpHost string) string {
	return configXML(
		imapXML(imapHost, 993, "SSL", "%EMAILADDRESS%"),
		smtpXML(smtpHost, 587, "STARTTLS", "%EMAILADDRESS%"),
	)
}

// expected is the MailServer that goodConfig gives for user.
func expected(imapHost, smtpHost, user string) account.MailServer {
	return account.MailServer{
		IMAPHost:     imapHost,
		IMAPPort:     993,
		IMAPSecurity: account.SecurityTLS,
		SMTPHost:     smtpHost,
		SMTPPort:     587,
		SMTPSecurity: account.SecuritySTARTTLS,
		Username:     user,
	}
}

func xmlHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		_, _ = io.WriteString(w, body)
	}
}

// --- helpers: servers ---

// requestLog records what the server receives.
type requestLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *requestLog) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		b.WriteString(r.Method + " " + r.RequestURI + " host=" + r.Host + "\n")
		for name, values := range r.Header {
			b.WriteString(name + ": " + strings.Join(values, ",") + "\n")
		}
		l.mu.Lock()
		l.lines = append(l.lines, b.String())
		l.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func (l *requestLog) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func (l *requestLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.lines)
}

// startServer starts a test server (https when tls is true) and closes it at
// the end of the test. The server does not write to the log: the client may
// open a spare connection that the server drops when it closes, and the
// standard library logs that as a handshake error.
func startServer(t *testing.T, handler http.Handler, tls bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(handler)
	srv.Config.ErrorLog = stdlog.New(io.Discard, "", 0)
	if tls {
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)
	return srv
}

// newTLSServer starts an https server for the test.
func newTLSServer(t *testing.T, mux *http.ServeMux) (*httptest.Server, *requestLog) {
	t.Helper()
	log := &requestLog{}
	return startServer(t, log.wrap(mux), true), log
}

// newFinder points the finder at srv for both HTTP sources.
func newFinder(srv *httptest.Server, dns Resolver) *Finder {
	return &Finder{
		HTTP:      srv.Client(),
		DNS:       dns,
		ISPDBBase: srv.URL + "/v1.1/",
		AutoconfigURL: func(domain string) string {
			return srv.URL + "/autoconfig/" + domain + "/config-v1.1.xml"
		},
	}
}

// --- helpers: DNS ---

type fakeDNS struct {
	mu      sync.Mutex
	srv     map[string][]*net.SRV // key is the service name, for example "imaps"
	mx      []*net.MX
	mxErr   error
	queries []string
}

func (d *fakeDNS) LookupSRV(_ context.Context, service, proto, name string) (string, []*net.SRV, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.queries = append(d.queries, "srv "+service+" "+proto+" "+name)
	if proto == "tcp" {
		if records, ok := d.srv[service]; ok {
			return "", records, nil
		}
	}
	return "", nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (d *fakeDNS) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.queries = append(d.queries, "mx "+name)
	if d.mxErr != nil {
		return nil, d.mxErr
	}
	if d.mx == nil {
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	return d.mx, nil
}

func (d *fakeDNS) allQueries() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	// Every query ends with a newline, so that a test can match a whole name
	// and not only the start of it.
	return strings.Join(d.queries, "\n") + "\n"
}

func srvRecord(priority, weight, port uint16, target string) *net.SRV {
	return &net.SRV{Priority: priority, Weight: weight, Port: port, Target: target}
}

// --- helpers: misc ---

func requireNoPlain(t *testing.T, servers []account.MailServer) {
	t.Helper()
	for _, s := range servers {
		require.NotEqual(t, account.SecurityPlain, s.IMAPSecurity)
		require.NotEqual(t, account.SecurityPlain, s.SMTPSecurity)
		require.NoError(t, s.Validate(true))
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// --- the order of the sources ---

func TestISPDBFirst(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.1/example.com", xmlHandler(goodConfig("imap.ispdb.example.org", "smtp.ispdb.example.org")))
	mux.HandleFunc("/autoconfig/example.com/config-v1.1.xml",
		xmlHandler(goodConfig("imap.auto.example.net", "smtp.auto.example.net")))
	srv, _ := newTLSServer(t, mux)
	dns := &fakeDNS{
		srv: map[string][]*net.SRV{
			"imaps":      {srvRecord(10, 1, 993, "imap.srv.example.com.")},
			"submission": {srvRecord(10, 1, 587, "smtp.srv.example.com.")},
		},
		mx: []*net.MX{{Host: "mx.mailhost.example.io.", Pref: 10}},
	}

	got, err := newFinder(srv, dns).Mail(context.Background(), testAddress)

	require.NoError(t, err)
	require.Equal(t, []account.MailServer{
		expected("imap.ispdb.example.org", "smtp.ispdb.example.org", testAddress),
		expected("imap.auto.example.net", "smtp.auto.example.net", testAddress),
		expected("imap.srv.example.com", "smtp.srv.example.com", testAddress),
		expected("imap.example.io", "smtp.example.io", testAddress),
	}, got)
	requireNoPlain(t, got)
}

func TestRemovesDuplicatesAcrossSources(t *testing.T) {
	mux := http.NewServeMux()
	same := goodConfig("imap.example.com", "smtp.example.com")
	mux.HandleFunc("/v1.1/example.com", xmlHandler(same))
	mux.HandleFunc("/autoconfig/example.com/config-v1.1.xml", xmlHandler(same))
	srv, _ := newTLSServer(t, mux)
	dns := &fakeDNS{
		srv: map[string][]*net.SRV{
			"imaps":      {srvRecord(0, 0, 993, "imap.example.com.")},
			"submission": {srvRecord(0, 0, 587, "smtp.example.com.")},
		},
		mx: []*net.MX{{Host: "mx.example.com.", Pref: 10}},
	}

	got, err := newFinder(srv, dns).Mail(context.Background(), testAddress)

	require.NoError(t, err)
	require.Equal(t, []account.MailServer{expected("imap.example.com", "smtp.example.com", testAddress)}, got)
}

func TestFallsBackToSRV(t *testing.T) {
	srv, _ := newTLSServer(t, http.NewServeMux()) // every path answers 404
	dns := &fakeDNS{
		srv: map[string][]*net.SRV{
			"imaps":       {srvRecord(10, 5, 993, "imap.example.net.")},
			"submissions": {srvRecord(10, 5, 465, "smtp.example.net.")},
		},
	}

	got, err := newFinder(srv, dns).Mail(context.Background(), testAddress)

	require.NoError(t, err)
	require.Equal(t, []account.MailServer{{
		IMAPHost: "imap.example.net", IMAPPort: 993, IMAPSecurity: account.SecurityTLS,
		SMTPHost: "smtp.example.net", SMTPPort: 465, SMTPSecurity: account.SecurityTLS,
		Username: testAddress,
	}}, got)
	queries := dns.allQueries()
	require.Contains(t, queries, "srv imaps tcp example.com.\n")
	require.Contains(t, queries, "srv submissions tcp example.com.\n")
	require.Contains(t, queries, "srv submission tcp example.com.\n")
}

func TestMXGuessLast(t *testing.T) {
	tests := []struct {
		name string
		mx   []*net.MX
		want []account.MailServer
	}{
		{
			name: "lowest preference wins",
			mx: []*net.MX{
				{Host: "mx2.backup.example.org.", Pref: 20},
				{Host: "mx1.mailhost.example.net.", Pref: 10},
			},
			want: []account.MailServer{expected("imap.example.net", "smtp.example.net", testAddress)},
		},
		{
			name: "base domain follows the public suffix list",
			mx:   []*net.MX{{Host: "MX.Mail.Example.CO.UK.", Pref: 5}},
			want: []account.MailServer{expected("imap.example.co.uk", "smtp.example.co.uk", testAddress)},
		},
		{
			name: "null MX gives no guess",
			mx:   []*net.MX{{Host: ".", Pref: 0}},
			want: nil,
		},
		{
			name: "a public suffix has no base domain",
			mx:   []*net.MX{{Host: "co.uk.", Pref: 5}},
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newTLSServer(t, http.NewServeMux())
			dns := &fakeDNS{mx: tc.mx}

			got, err := newFinder(srv, dns).Mail(context.Background(), testAddress)

			if tc.want == nil {
				require.ErrorIs(t, err, ErrNotFound)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestMXGuessComesAfterOtherSources(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.1/example.com", xmlHandler(goodConfig("imap.ispdb.example.org", "smtp.ispdb.example.org")))
	srv, _ := newTLSServer(t, mux)
	dns := &fakeDNS{mx: []*net.MX{{Host: "mx.mailhost.example.io.", Pref: 10}}}

	got, err := newFinder(srv, dns).Mail(context.Background(), testAddress)

	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "imap.ispdb.example.org", got[0].IMAPHost)
	require.Equal(t, "imap.example.io", got[1].IMAPHost)
}

// --- security: nothing plain ---

func TestIgnoresPlainSuggestions(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []account.MailServer
	}{
		{
			name: "plain IMAP and plain SMTP",
			body: configXML(imapXML("imap.example.com", 143, "plain", ""), smtpXML("smtp.example.com", 25, "plain", "")),
		},
		{
			name: "TLS IMAP with plain SMTP only",
			body: configXML(imapXML("imap.example.com", 993, "SSL", ""), smtpXML("smtp.example.com", 25, "plain", "")),
		},
		{
			name: "unknown socket type",
			body: configXML(imapXML("imap.example.com", 993, "maybe", ""), smtpXML("smtp.example.com", 587, "STARTTLS", "")),
		},
		{
			name: "plain server is skipped and the encrypted one is kept",
			body: configXML(
				imapXML("imap.example.com", 143, "plain", ""),
				imapXML("imap.example.com", 993, "SSL", ""),
				smtpXML("smtp.example.com", 25, "plain", ""),
				smtpXML("smtp.example.com", 587, "STARTTLS", ""),
			),
			want: []account.MailServer{expected("imap.example.com", "smtp.example.com", testAddress)},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/v1.1/example.com", xmlHandler(tc.body))
			srv, _ := newTLSServer(t, mux)
			// A plain IMAP record must never be used, even when it is the only one.
			dns := &fakeDNS{srv: map[string][]*net.SRV{
				"imap":       {srvRecord(0, 0, 143, "imap.plain.example.com.")},
				"submission": {srvRecord(0, 0, 587, "smtp.plain.example.com.")},
			}}

			got, err := newFinder(srv, dns).Mail(context.Background(), testAddress)

			requireNoPlain(t, got)
			if tc.want == nil {
				require.ErrorIs(t, err, ErrNotFound)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.want, got)
			}
			require.NotContains(t, dns.allQueries(), "srv imap tcp", "the plain IMAP service must not be asked for")
		})
	}
}

// --- bounds ---

// paddedConfig returns a valid config that is exactly size bytes long.
func paddedConfig(t *testing.T, size int) string {
	t.Helper()
	base := goodConfig("imap.example.com", "smtp.example.com")
	const overhead = len("<!---->")
	pad := size - len(base) - overhead
	require.GreaterOrEqual(t, pad, 0)
	body := base + "<!--" + strings.Repeat("a", pad) + "-->"
	require.Len(t, body, size)
	return body
}

func TestResponseSizeCapped(t *testing.T) {
	tests := []struct {
		name      string
		size      int
		wantFound bool
	}{
		{"exactly 1 MiB is read", 1 << 20, true},
		{"one byte over 1 MiB is skipped", 1<<20 + 1, false},
		{"far over 1 MiB is skipped", 4 << 20, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/v1.1/example.com", xmlHandler(paddedConfig(t, tc.size)))
			srv, _ := newTLSServer(t, mux)

			got, err := newFinder(srv, &fakeDNS{}).Mail(context.Background(), testAddress)

			if tc.wantFound {
				require.NoError(t, err)
				require.Equal(t, []account.MailServer{expected("imap.example.com", "smtp.example.com", testAddress)}, got)
				return
			}
			require.ErrorIs(t, err, ErrNotFound)
			require.Empty(t, got)
		})
	}
}

// countingBody is an endless response body that counts the bytes read from it.
type countingBody struct{ read *atomic.Int64 }

func (b countingBody) Read(p []byte) (int, error) {
	b.read.Add(int64(len(p)))
	return len(p), nil // fills p with zero bytes without end
}

func (countingBody) Close() error { return nil }

func TestReadStopsAtTheLimit(t *testing.T) {
	var read atomic.Int64
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       countingBody{read: &read},
			Request:    r,
		}, nil
	})}
	finder := &Finder{HTTP: client}

	_, err := finder.fetch(context.Background(), "https://example.com/config")

	require.ErrorIs(t, err, errTooLarge)
	// The reader may ask for a little more than the limit in its last read, but
	// it must not read an endless body.
	require.LessOrEqual(t, read.Load(), int64(maxBodyBytes+1))
}

func TestWholeLookupHasATimeout(t *testing.T) {
	mux := http.NewServeMux()
	block := func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}
	mux.HandleFunc("/v1.1/", block)
	mux.HandleFunc("/autoconfig/", block)
	srv, _ := newTLSServer(t, mux)
	finder := newFinder(srv, &fakeDNS{})
	finder.timeout = 150 * time.Millisecond

	start := time.Now()
	_, err := finder.Mail(context.Background(), testAddress)

	require.ErrorIs(t, err, ErrNotFound)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 5*time.Second)
}

func TestCallerDeadlineIsHonoured(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	})
	srv, _ := newTLSServer(t, mux)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := newFinder(srv, &fakeDNS{}).Mail(ctx, testAddress)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 5*time.Second)
}

func TestSlowSourceDoesNotStarveTheOthers(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	})
	srv, _ := newTLSServer(t, mux)
	dns := &fakeDNS{mx: []*net.MX{{Host: "mx.mailhost.example.io.", Pref: 10}}}
	finder := newFinder(srv, dns)
	finder.timeout = 300 * time.Millisecond

	got, err := finder.Mail(context.Background(), testAddress)

	require.NoError(t, err)
	require.Equal(t, []account.MailServer{expected("imap.example.io", "smtp.example.io", testAddress)}, got)
}

func TestSuggestionsPerSourceAreBounded(t *testing.T) {
	var parts []string
	for i := range 20 {
		parts = append(parts, imapXML(fmt.Sprintf("imap%d.example.com", i), 993, "SSL", ""))
		parts = append(parts, smtpXML(fmt.Sprintf("smtp%d.example.com", i), 587, "STARTTLS", ""))
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.1/example.com", xmlHandler(configXML(parts...)))
	srv, _ := newTLSServer(t, mux)

	got, err := newFinder(srv, &fakeDNS{}).Mail(context.Background(), testAddress)

	require.NoError(t, err)
	require.Len(t, got, maxServersPerSide)
	require.LessOrEqual(t, len(got), maxSuggestions)
}

// --- privacy ---

func TestEmailAddressNeverLeavesTheMachine(t *testing.T) {
	mux := http.NewServeMux()
	// The config asks for the local part as user name. That is only for the
	// result. It must not change what is sent.
	cfg := configXML(
		imapXML("imap.example.com", 993, "SSL", "%EMAILLOCALPART%"),
		smtpXML("smtp.example.com", 587, "STARTTLS", "%EMAILADDRESS%"),
	)
	mux.HandleFunc("/v1.1/example.com", xmlHandler(cfg))
	mux.HandleFunc("/autoconfig/example.com/config-v1.1.xml", xmlHandler(cfg))
	srv, log := newTLSServer(t, mux)
	dns := &fakeDNS{
		srv: map[string][]*net.SRV{
			"imaps":      {srvRecord(0, 0, 993, "imap.example.com.")},
			"submission": {srvRecord(0, 0, 587, "smtp.example.com.")},
		},
		mx: []*net.MX{{Host: "mx.mailhost.example.io.", Pref: 10}},
	}

	got, err := newFinder(srv, dns).Mail(context.Background(), testAddress)

	require.NoError(t, err)
	require.NotEmpty(t, got)
	require.Equal(t, 2, log.count(), "both HTTP sources are asked")
	for _, secret := range []string{testAddress, "zebra", "crossing", "%40", "@"} {
		require.NotContains(t, log.all(), secret, "an HTTP request contains part of the address")
		require.NotContains(t, dns.allQueries(), secret, "a DNS query contains part of the address")
	}
	require.NotContains(t, log.all(), "emailaddress")
	// The request line has no query string at all.
	require.NotContains(t, log.all(), "?")
}

func TestRequestsOnlyCarryTheDomain(t *testing.T) {
	mux := http.NewServeMux()
	srv, log := newTLSServer(t, mux)
	dns := &fakeDNS{}

	_, err := newFinder(srv, dns).Mail(context.Background(), "Zebra.Crossing@EXAMPLE.com")

	require.ErrorIs(t, err, ErrNotFound)
	require.Contains(t, log.all(), "GET /v1.1/example.com ")
	require.Contains(t, log.all(), "GET /autoconfig/example.com/config-v1.1.xml ")
	require.NotContains(t, strings.ToLower(log.all()), "zebra")
	require.Contains(t, dns.allQueries(), "mx example.com.\n")
}

func TestIDNDomainIsSentAsPunycode(t *testing.T) {
	const punycode = "xn--bcher-kva.de"
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.1/"+punycode, xmlHandler(goodConfig("imap.example.com", "smtp.example.com")))
	srv, log := newTLSServer(t, mux)
	dns := &fakeDNS{}
	finder := newFinder(srv, dns)
	var hookDomain string
	finder.AutoconfigURL = func(domain string) string {
		hookDomain = domain
		return srv.URL + "/autoconfig/" + domain
	}
	address := "Jörg@bücher.de"

	got, err := finder.Mail(context.Background(), address)

	require.NoError(t, err)
	// The user name stays as the user typed it. Only the lookups change.
	require.Equal(t, []account.MailServer{expected("imap.example.com", "smtp.example.com", address)}, got)
	require.Equal(t, punycode, hookDomain)
	require.Contains(t, log.all(), "GET /v1.1/"+punycode+" ")
	require.NotContains(t, log.all(), "ü")
	require.Contains(t, dns.allQueries(), "mx "+punycode+".\n")
	require.Contains(t, dns.allQueries(), "srv imaps tcp "+punycode+".\n")
}

// --- https only ---

func TestRedirectToHTTPRefused(t *testing.T) {
	var plainHits atomic.Int32
	plain := startServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainHits.Add(1)
		xmlHandler(goodConfig("imap.evil.example", "smtp.evil.example"))(w, r)
	}), false)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1.1/example.com", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/config", http.StatusFound)
	})
	mux.HandleFunc("/autoconfig/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/config", http.StatusMovedPermanently)
	})
	srv, _ := newTLSServer(t, mux)
	finder := newFinder(srv, &fakeDNS{})

	got, err := finder.Mail(context.Background(), testAddress)

	require.ErrorIs(t, err, ErrNotFound)
	require.Empty(t, got)
	require.Zero(t, plainHits.Load(), "the plain http server must not be contacted")
	// The caller's client is not changed.
	require.Nil(t, finder.HTTP.CheckRedirect)
}

func TestRedirectsToHTTPSAreLimitedToThree(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/hop/", func(w http.ResponseWriter, r *http.Request) {
		n, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/hop/"))
		if err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		if n == 0 {
			xmlHandler(goodConfig("imap.example.com", "smtp.example.com"))(w, r)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/hop/%d", n-1), http.StatusFound)
	})
	srv, _ := newTLSServer(t, mux)

	tests := []struct {
		redirects int
		wantFound bool
	}{
		{redirects: 1, wantFound: true},
		{redirects: 3, wantFound: true},
		{redirects: 4, wantFound: false},
		{redirects: 9, wantFound: false},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("%d redirects", tc.redirects), func(t *testing.T) {
			finder := newFinder(srv, &fakeDNS{})
			finder.ISPDBBase = srv.URL + "/unused/"
			finder.AutoconfigURL = func(string) string {
				return fmt.Sprintf("%s/hop/%d", srv.URL, tc.redirects)
			}

			got, err := finder.Mail(context.Background(), testAddress)

			if tc.wantFound {
				require.NoError(t, err)
				require.Len(t, got, 1)
			} else {
				require.ErrorIs(t, err, ErrNotFound)
			}
		})
	}
}

func TestNonHTTPSURLsAreNeverFetched(t *testing.T) {
	var hits atomic.Int32
	plain := startServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		xmlHandler(goodConfig("imap.example.com", "smtp.example.com"))(w, r)
	}), false)
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("no network in this test")
	})}

	for _, rawURL := range []string{plain.URL + "/config", "ftp://example.com/config", "https:///nohost", "//example.com/config", "example.com/config"} {
		t.Run(rawURL, func(t *testing.T) {
			finder := &Finder{
				HTTP:          client,
				DNS:           &fakeDNS{},
				ISPDBBase:     rawURL,
				AutoconfigURL: func(string) string { return rawURL },
			}

			_, err := finder.Mail(context.Background(), testAddress)

			require.ErrorIs(t, err, ErrNotFound)
		})
	}
	require.Zero(t, hits.Load())
	require.Zero(t, calls.Load(), "no request must reach the client")
}

// --- config parsing ---

func TestRejectsBadHostnamesInConfig(t *testing.T) {
	bad := []string{
		"https://imap.example.com",
		"imap://imap.example.com",
		"imap.example.com/inbox",
		"imap.example.com\\inbox",
		"imap example.com",
		"imap.example.com:993",
		"%HOSTNAME%",
		"%EMAILDOMAIN%",
		"imap.%EMAILDOMAIN%",
		"user@imap.example.com",
		"imap.example.com.",
		".imap.example.com",
		"imap..example.com",
		"-imap.example.com",
		"imap-.example.com",
		"127.0.0.1",
		"192.168.0.10",
		"[::1]",
		"::1",
		"localhost",
		"mail",
		// A reserved last label is refused, also with other labels before it
		// (RFC 6761 says that *.localhost is loopback) and in any case.
		"imap.localhost",
		"imap.local",
		"imap.localdomain",
		"imap.internal",
		"imap.invalid",
		"imap.test",
		"imap.example",
		"a.b.localhost",
		"IMAP.LOCALHOST",
		"Imap.Internal",
		"imap.Example",
		"imap_1.example.com",
		strings.Repeat("a", 64) + ".example.com",
		"",
	}
	for _, host := range bad {
		t.Run("imap "+host, func(t *testing.T) {
			mux := http.NewServeMux()
			body := configXML(imapXML(host, 993, "SSL", ""), smtpXML("smtp.example.com", 587, "STARTTLS", ""))
			mux.HandleFunc("/v1.1/example.com", xmlHandler(body))
			srv, _ := newTLSServer(t, mux)

			got, err := newFinder(srv, &fakeDNS{}).Mail(context.Background(), testAddress)

			require.ErrorIs(t, err, ErrNotFound)
			require.Empty(t, got)
		})
		t.Run("smtp "+host, func(t *testing.T) {
			mux := http.NewServeMux()
			body := configXML(imapXML("imap.example.com", 993, "SSL", ""), smtpXML(host, 587, "STARTTLS", ""))
			mux.HandleFunc("/v1.1/example.com", xmlHandler(body))
			srv, _ := newTLSServer(t, mux)

			got, err := newFinder(srv, &fakeDNS{}).Mail(context.Background(), testAddress)

			require.ErrorIs(t, err, ErrNotFound)
			require.Empty(t, got)
		})
	}
}

func TestRejectsBadPortsInConfig(t *testing.T) {
	for _, port := range []string{"0", "-1", "65536", "99999999999", "abc", "", "99 3", "1e3"} {
		t.Run("port "+port, func(t *testing.T) {
			imap := `<incomingServer type="imap"><hostname>imap.example.com</hostname><port>` + port +
				`</port><socketType>SSL</socketType></incomingServer>`
			mux := http.NewServeMux()
			mux.HandleFunc("/v1.1/example.com",
				xmlHandler(configXML(imap, smtpXML("smtp.example.com", 587, "STARTTLS", ""))))
			srv, _ := newTLSServer(t, mux)

			_, err := newFinder(srv, &fakeDNS{}).Mail(context.Background(), testAddress)

			require.ErrorIs(t, err, ErrNotFound)
		})
	}
}

func TestAcceptsPortLimits(t *testing.T) {
	for _, port := range []int{1, 65535} {
		t.Run(strconv.Itoa(port), func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/v1.1/example.com", xmlHandler(configXML(
				imapXML("imap.example.com", port, "SSL", ""),
				smtpXML("smtp.example.com", port, "SSL", ""),
			)))
			srv, _ := newTLSServer(t, mux)

			got, err := newFinder(srv, &fakeDNS{}).Mail(context.Background(), testAddress)

			require.NoError(t, err)
			require.Len(t, got, 1)
			require.Equal(t, port, got[0].IMAPPort)
			require.Equal(t, port, got[0].SMTPPort)
		})
	}
}

func TestSocketTypesAndServerTypes(t *testing.T) {
	body := configXML(
		// POP3 and other server types are ignored.
		`<incomingServer type="pop3"><hostname>pop.example.com</hostname><port>995</port><socketType>SSL</socketType></incomingServer>`,
		`<incomingServer type="exchange"><hostname>ex.example.com</hostname><port>443</port><socketType>SSL</socketType></incomingServer>`,
		imapXML("imap.example.com", 143, "STARTTLS", ""),
		smtpXML("smtp.example.com", 465, "SSL", ""),
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.1/example.com", xmlHandler(body))
	srv, _ := newTLSServer(t, mux)

	got, err := newFinder(srv, &fakeDNS{}).Mail(context.Background(), testAddress)

	require.NoError(t, err)
	require.Equal(t, []account.MailServer{{
		IMAPHost: "imap.example.com", IMAPPort: 143, IMAPSecurity: account.SecuritySTARTTLS,
		SMTPHost: "smtp.example.com", SMTPPort: 465, SMTPSecurity: account.SecurityTLS,
		Username: testAddress,
	}}, got)
}

func TestSeveralServersArePairedInOrderWithTLSFirst(t *testing.T) {
	body := configXML(
		imapXML("imap.example.com", 143, "STARTTLS", ""),
		imapXML("imap.example.com", 993, "SSL", ""),
		smtpXML("smtp.example.com", 587, "STARTTLS", ""),
		smtpXML("smtp.example.com", 465, "SSL", ""),
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.1/example.com", xmlHandler(body))
	srv, _ := newTLSServer(t, mux)

	got, err := newFinder(srv, &fakeDNS{}).Mail(context.Background(), testAddress)

	require.NoError(t, err)
	require.Equal(t, []account.MailServer{
		{
			IMAPHost: "imap.example.com", IMAPPort: 993, IMAPSecurity: account.SecurityTLS,
			SMTPHost: "smtp.example.com", SMTPPort: 465, SMTPSecurity: account.SecurityTLS,
			Username: testAddress,
		},
		{
			IMAPHost: "imap.example.com", IMAPPort: 143, IMAPSecurity: account.SecuritySTARTTLS,
			SMTPHost: "smtp.example.com", SMTPPort: 587, SMTPSecurity: account.SecuritySTARTTLS,
			Username: testAddress,
		},
	}, got)
}

func TestEveryServerIsUsedWhenTheSidesDiffer(t *testing.T) {
	// One IMAP server and two SMTP servers, like many public providers.
	body := configXML(
		imapXML("imap.example.com", 993, "SSL", ""),
		smtpXML("smtp.example.com", 465, "SSL", ""),
		smtpXML("smtp.example.com", 587, "STARTTLS", ""),
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.1/example.com", xmlHandler(body))
	srv, _ := newTLSServer(t, mux)

	got, err := newFinder(srv, &fakeDNS{}).Mail(context.Background(), testAddress)

	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, 465, got[0].SMTPPort)
	require.Equal(t, 587, got[1].SMTPPort)
	require.Equal(t, "imap.example.com", got[0].IMAPHost)
	require.Equal(t, "imap.example.com", got[1].IMAPHost)
}

func TestOneSideOnlyContributesNothing(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.1/example.com", xmlHandler(configXML(imapXML("imap.example.com", 993, "SSL", ""))))
	mux.HandleFunc("/autoconfig/example.com/config-v1.1.xml",
		xmlHandler(configXML(smtpXML("smtp.example.com", 587, "STARTTLS", ""))))
	srv, _ := newTLSServer(t, mux)

	_, err := newFinder(srv, &fakeDNS{}).Mail(context.Background(), testAddress)

	// The IMAP half from one source must not be joined to the SMTP half from another.
	require.ErrorIs(t, err, ErrNotFound)
}

func TestUserNameTemplates(t *testing.T) {
	const address = "Jane.Doe@example.com"
	tests := []struct {
		name     string
		template string
		want     string
	}{
		{"none", "", address},
		{"address", "%EMAILADDRESS%", address},
		{"local part", "%EMAILLOCALPART%", "Jane.Doe"},
		{"domain", "%EMAILDOMAIN%", "example.com"},
		{"mixed", "%EMAILLOCALPART%@corp.%EMAILDOMAIN%", "Jane.Doe@corp.example.com"},
		{"fixed", "mailbox-17", "mailbox-17"},
		{"unknown placeholder", "%USERNAME%", address},
		{"leftover placeholder", "%EMAILLOCALPART%%X%", address},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/v1.1/example.com", xmlHandler(configXML(
				imapXML("imap.example.com", 993, "SSL", tc.template),
				smtpXML("smtp.example.com", 587, "STARTTLS", "%EMAILADDRESS%"),
			)))
			srv, _ := newTLSServer(t, mux)

			got, err := newFinder(srv, &fakeDNS{}).Mail(context.Background(), address)

			require.NoError(t, err)
			require.Len(t, got, 1)
			require.Equal(t, tc.want, got[0].Username)
		})
	}
}

func TestExternalEntitiesAreNotResolved(t *testing.T) {
	var fetched atomic.Int32
	other := startServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetched.Add(1)
	}), true)
	body := `<?xml version="1.0"?><!DOCTYPE clientConfig [<!ENTITY host SYSTEM "` + other.URL + `/x">]>` +
		`<clientConfig version="1.1"><emailProvider>` +
		imapXML("&host;", 993, "SSL", "") + smtpXML("smtp.example.com", 587, "STARTTLS", "") +
		`</emailProvider></clientConfig>`
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.1/example.com", xmlHandler(body))
	srv, _ := newTLSServer(t, mux)

	_, err := newFinder(srv, &fakeDNS{}).Mail(context.Background(), testAddress)

	require.ErrorIs(t, err, ErrNotFound)
	require.Zero(t, fetched.Load())
}

// --- SRV ---

func TestSRVPriorityOrderAndDotTargets(t *testing.T) {
	srv, _ := newTLSServer(t, http.NewServeMux())
	dns := &fakeDNS{srv: map[string][]*net.SRV{
		// The records are not in order. Lowest priority comes first. Inside a
		// priority, the higher weight comes first.
		"imaps": {
			srvRecord(20, 5, 993, "imap-c.example.net."),
			srvRecord(10, 1, 993, "imap-b.example.net."),
			srvRecord(10, 9, 993, "imap-a.example.net."),
		},
		// "." means that the service is not offered.
		"submissions": {srvRecord(0, 0, 465, ".")},
		"submission": {
			srvRecord(30, 0, 587, "smtp-b.example.net."),
			srvRecord(5, 0, 587, "smtp-a.example.net."),
			srvRecord(1, 0, 587, "."),
		},
	}}

	got, err := newFinder(srv, dns).Mail(context.Background(), testAddress)

	require.NoError(t, err)
	require.Equal(t, []account.MailServer{
		expected("imap-a.example.net", "smtp-a.example.net", testAddress),
		expected("imap-b.example.net", "smtp-b.example.net", testAddress),
		expected("imap-c.example.net", "smtp-b.example.net", testAddress),
	}, got)
}

func TestSRVSecurityPerService(t *testing.T) {
	srv, _ := newTLSServer(t, http.NewServeMux())
	dns := &fakeDNS{srv: map[string][]*net.SRV{
		"imaps":       {srvRecord(0, 0, 993, "imap.example.net.")},
		"submissions": {srvRecord(0, 0, 465, "smtp.example.net.")},
		"submission":  {srvRecord(0, 0, 587, "smtp.example.net.")},
	}}

	got, err := newFinder(srv, dns).Mail(context.Background(), testAddress)

	require.NoError(t, err)
	require.Len(t, got, 2)
	// Implicit TLS is listed before STARTTLS.
	require.Equal(t, account.SecurityTLS, got[0].SMTPSecurity)
	require.Equal(t, 465, got[0].SMTPPort)
	require.Equal(t, account.SecuritySTARTTLS, got[1].SMTPSecurity)
	require.Equal(t, 587, got[1].SMTPPort)
	for _, s := range got {
		require.Equal(t, account.SecurityTLS, s.IMAPSecurity)
	}
}

func TestSRVNeedsBothSides(t *testing.T) {
	srv, _ := newTLSServer(t, http.NewServeMux())

	tests := []struct {
		name string
		srv  map[string][]*net.SRV
	}{
		{"only IMAP", map[string][]*net.SRV{"imaps": {srvRecord(0, 0, 993, "imap.example.net.")}}},
		{"only SMTP", map[string][]*net.SRV{"submission": {srvRecord(0, 0, 587, "smtp.example.net.")}}},
		{"all targets are dots", map[string][]*net.SRV{
			"imaps":       {srvRecord(0, 0, 993, ".")},
			"submissions": {srvRecord(0, 0, 465, ".")},
			"submission":  {srvRecord(0, 0, 587, ".")},
		}},
		{"port zero", map[string][]*net.SRV{
			"imaps":      {srvRecord(0, 0, 0, "imap.example.net.")},
			"submission": {srvRecord(0, 0, 587, "smtp.example.net.")},
		}},
		{"bad target", map[string][]*net.SRV{
			"imaps":      {srvRecord(0, 0, 993, "https://evil.example.net/.")},
			"submission": {srvRecord(0, 0, 587, "smtp.example.net.")},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newFinder(srv, &fakeDNS{srv: tc.srv}).Mail(context.Background(), testAddress)

			require.ErrorIs(t, err, ErrNotFound)
		})
	}
}

func TestSRVPartialResultsWithAnErrorAreUsed(t *testing.T) {
	// The standard resolver returns the good records together with an error
	// when it drops some bad records.
	srv, _ := newTLSServer(t, http.NewServeMux())
	dns := &fakeDNS{
		srv: map[string][]*net.SRV{
			"imaps":      {srvRecord(0, 0, 993, "imap.example.net.")},
			"submission": {srvRecord(0, 0, 587, "smtp.example.net.")},
		},
	}
	partial := &partialDNS{fakeDNS: dns}

	got, err := newFinder(srv, partial).Mail(context.Background(), testAddress)

	require.NoError(t, err)
	require.Len(t, got, 1)
}

// partialDNS returns records and an error together.
type partialDNS struct{ *fakeDNS }

func (p *partialDNS) LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error) {
	cname, records, err := p.fakeDNS.LookupSRV(ctx, service, proto, name)
	if len(records) > 0 {
		return cname, records, &net.DNSError{Err: "some records were not valid", Name: name}
	}
	return cname, records, err
}

// --- failures move on ---

func TestOneSourceFailingMovesOnToTheNext(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.1/example.com", xmlHandler("<clientConfig><emailProvider>")) // bad XML
	mux.HandleFunc("/autoconfig/example.com/config-v1.1.xml", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	srv, _ := newTLSServer(t, mux)
	dns := &fakeDNS{
		srv: map[string][]*net.SRV{
			"imaps":      {srvRecord(0, 0, 993, "imap.example.net.")},
			"submission": {srvRecord(0, 0, 587, "smtp.example.net.")},
		},
		mxErr: errors.New("dns is down"),
	}

	got, err := newFinder(srv, dns).Mail(context.Background(), testAddress)

	require.NoError(t, err)
	require.Equal(t, []account.MailServer{expected("imap.example.net", "smtp.example.net", testAddress)}, got)
}

func TestNetworkErrorIsNotFatal(t *testing.T) {
	srv, _ := newTLSServer(t, http.NewServeMux())
	finder := newFinder(srv, &fakeDNS{mx: []*net.MX{{Host: "mx.mailhost.example.io.", Pref: 1}}})
	finder.HTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})}

	got, err := finder.Mail(context.Background(), testAddress)

	require.NoError(t, err)
	require.Len(t, got, 1)
}

func TestNoSourceGivesErrNotFound(t *testing.T) {
	srv, _ := newTLSServer(t, http.NewServeMux())

	got, err := newFinder(srv, &fakeDNS{}).Mail(context.Background(), testAddress)

	require.ErrorIs(t, err, ErrNotFound)
	require.Nil(t, got)
	require.NotContains(t, err.Error(), "zebra")
}

// --- the address ---

func TestInvalidAddressesAreRejectedBeforeAnyNetworkUse(t *testing.T) {
	tooLongLabel := strings.Repeat("a", 64) + ".com"
	tests := []string{
		"",
		"   ",
		"plainaddress",
		"@example.com",
		"user@",
		"user@localhost",
		"user@mail.localhost",
		"user@mail.local",
		"user@mail.localdomain",
		"user@corp.internal",
		"user@mail.invalid",
		"user@mail.test",
		"user@mail.example",
		"user@MAIL.LOCALHOST",
		"user@example",
		"user@example.com.",
		"user@.example.com",
		"user@example..com",
		"user@-example.com",
		"user@https://example.com",
		"user@example.com/path",
		"user@example.com:993",
		"user@example.com?x=1",
		"user@example.com#x",
		"user@exam ple.com",
		"user name@example.com",
		"us\ner@example.com",
		"user@exa\x00mple.com",
		"user@192.168.1.1",
		"user@[::1]",
		"user@[192.168.1.1]",
		"user@1234",
		"user@" + tooLongLabel,
		"user@" + strings.Repeat("a.", 130) + "com",
		strings.Repeat("a", 300) + "@example.com",
		"user@@@",
	}
	var calls atomic.Int32
	finder := &Finder{
		HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, errors.New("no network")
		})},
		DNS: &failingDNS{calls: &calls},
	}
	for _, address := range tests {
		t.Run(fmt.Sprintf("%q", address), func(t *testing.T) {
			got, err := finder.Mail(context.Background(), address)

			require.ErrorIs(t, err, ErrInvalidAddress)
			require.Nil(t, got)
			if strings.TrimSpace(address) != "" {
				require.NotContains(t, err.Error(), strings.TrimSpace(address), "the error must not repeat the address")
			}
		})
	}
	require.Zero(t, calls.Load(), "no lookup must happen for an invalid address")
}

// failingDNS counts calls. A test expects zero.
type failingDNS struct{ calls *atomic.Int32 }

func (d *failingDNS) LookupSRV(context.Context, string, string, string) (string, []*net.SRV, error) {
	d.calls.Add(1)
	return "", nil, errors.New("no dns")
}

func (d *failingDNS) LookupMX(context.Context, string) ([]*net.MX, error) {
	d.calls.Add(1)
	return nil, errors.New("no dns")
}

func TestValidAddressForms(t *testing.T) {
	tests := []struct {
		address    string
		wantDomain string
	}{
		{"user@example.com", "example.com"},
		{"  user@example.com \n", "example.com"},
		{"User.Name+tag@Sub.Example.COM", "sub.example.com"},
		{"a@b@example.com", "example.com"}, // the part after the last @ is the domain
		{"user@xn--bcher-kva.de", "xn--bcher-kva.de"},
		{"user@bücher.de", "xn--bcher-kva.de"},
		{"user@a-b.example.co.uk", "a-b.example.co.uk"},
	}
	for _, tc := range tests {
		t.Run(tc.address, func(t *testing.T) {
			addr, err := parseAddress(tc.address)

			require.NoError(t, err)
			require.Equal(t, tc.wantDomain, addr.host)
			require.Equal(t, strings.TrimSpace(tc.address), addr.full)
		})
	}
}

// --- defaults ---

func TestDefaults(t *testing.T) {
	finder := &Finder{}

	client := finder.httpClient()
	require.Equal(t, 10*time.Second, client.Timeout)
	require.NotNil(t, client.CheckRedirect)
	httpReq := &http.Request{URL: mustParseURL(t, "http://example.com/")}
	require.Error(t, client.CheckRedirect(httpReq, []*http.Request{{}}))
	httpsReq := &http.Request{URL: mustParseURL(t, "https://example.com/")}
	require.NoError(t, client.CheckRedirect(httpsReq, []*http.Request{{}, {}, {}}))
	require.Error(t, client.CheckRedirect(httpsReq, []*http.Request{{}, {}, {}, {}}))

	require.Same(t, net.DefaultResolver, finder.dns())
	require.Equal(t, "https://autoconfig.thunderbird.net/v1.1/example.com", finder.ispdbURL("example.com"))
	require.Equal(t, "https://autoconfig.example.com/mail/config-v1.1.xml", finder.autoconfigURL("example.com"))
	require.Equal(t, 10*time.Second, finder.lookupTimeout())
}

func TestISPDBBaseWithoutTrailingSlash(t *testing.T) {
	finder := &Finder{ISPDBBase: "https://isp.example.org/v1.1"}

	require.Equal(t, "https://isp.example.org/v1.1/example.com", finder.ispdbURL("example.com"))
}

func TestCustomClientKeepsItsOwnRedirectPolicy(t *testing.T) {
	own := errors.New("own policy")
	custom := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return own }}
	finder := &Finder{HTTP: custom}
	client := finder.httpClient()

	httpsReq := &http.Request{URL: mustParseURL(t, "https://example.com/")}
	require.ErrorIs(t, client.CheckRedirect(httpsReq, []*http.Request{{}}), own)
	// The https rule still applies first.
	httpReq := &http.Request{URL: mustParseURL(t, "http://example.com/")}
	err := client.CheckRedirect(httpReq, []*http.Request{{}})
	require.Error(t, err)
	require.NotErrorIs(t, err, own)
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u
}

// --- reserved host names from DNS ---

// The same rule as for the config files applies to the targets of SRV records
// and to MX hosts: a host whose last label is reserved is never suggested.
func TestReservedHostsFromDNSAreIgnored(t *testing.T) {
	srv, _ := newTLSServer(t, http.NewServeMux())

	t.Run("srv", func(t *testing.T) {
		for _, bad := range []string{"imap.corp.internal.", "imap.mail.localhost.", "IMAP.MAIL.LOCAL."} {
			dns := &fakeDNS{srv: map[string][]*net.SRV{
				"imaps":      {srvRecord(0, 0, 993, bad)},
				"submission": {srvRecord(0, 0, 587, "smtp.example.net.")},
			}}

			_, err := newFinder(srv, dns).Mail(context.Background(), testAddress)

			require.ErrorIs(t, err, ErrNotFound, bad)
		}
	})
	t.Run("mx", func(t *testing.T) {
		for _, bad := range []string{"mx.corp.internal.", "mx.mail.localhost.", "MX.MAIL.TEST."} {
			dns := &fakeDNS{mx: []*net.MX{{Host: bad, Pref: 10}}}

			_, err := newFinder(srv, dns).Mail(context.Background(), testAddress)

			require.ErrorIs(t, err, ErrNotFound, bad)
		}
	})
}

// --- a stable order for equal DNS records ---

// rotatingDNS gives the records of fakeDNS in a different order on each
// round, as the Go resolver does for records with equal priority and weight
// (SRV) or equal preference (MX). The test changes shift between calls of Mail.
type rotatingDNS struct {
	*fakeDNS
	shift int
}

func rotated[T any](records []T, shift int) []T {
	if len(records) == 0 {
		return nil
	}
	shift %= len(records)
	return append(slices.Clone(records[shift:]), records[:shift]...)
}

func (d *rotatingDNS) LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error) {
	cname, records, err := d.fakeDNS.LookupSRV(ctx, service, proto, name)
	return cname, rotated(records, d.shift), err
}

func (d *rotatingDNS) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	records, err := d.fakeDNS.LookupMX(ctx, name)
	return rotated(records, d.shift), err
}

func TestEqualSRVRecordsGiveAStableOrder(t *testing.T) {
	srv, _ := newTLSServer(t, http.NewServeMux())
	// All records have the same priority and weight. The names have mixed case
	// on purpose: the order must not depend on the case.
	dns := &rotatingDNS{fakeDNS: &fakeDNS{srv: map[string][]*net.SRV{
		"imaps": {
			srvRecord(10, 5, 993, "Imap-b.example.net."),
			srvRecord(10, 5, 993, "imap-a.example.net."),
			srvRecord(10, 5, 993, "IMAP-C.example.net."),
		},
		"submissions": {
			srvRecord(10, 5, 465, "smtp-c.example.net."),
			srvRecord(10, 5, 465, "SMTP-A.example.net."),
			srvRecord(10, 5, 465, "Smtp-b.example.net."),
		},
	}}}
	want := []account.MailServer{
		smtpTLS(expected("imap-a.example.net", "smtp-a.example.net", testAddress)),
		smtpTLS(expected("imap-b.example.net", "smtp-b.example.net", testAddress)),
		smtpTLS(expected("imap-c.example.net", "smtp-c.example.net", testAddress)),
	}

	for shift := range 6 {
		dns.shift = shift

		got, err := newFinder(srv, dns).Mail(context.Background(), testAddress)

		require.NoError(t, err, "shift %d", shift)
		require.Equal(t, want, got, "shift %d", shift)
	}
}

// smtpTLS changes the SMTP side of a suggestion to implicit TLS on port 465.
func smtpTLS(s account.MailServer) account.MailServer {
	s.SMTPPort = 465
	s.SMTPSecurity = account.SecurityTLS
	return s
}

func TestEqualPriorityAndWeightKeepsPriorityFirst(t *testing.T) {
	srv, _ := newTLSServer(t, http.NewServeMux())
	// The tie-break by name must not beat priority or weight.
	dns := &rotatingDNS{fakeDNS: &fakeDNS{srv: map[string][]*net.SRV{
		"imaps": {
			srvRecord(10, 1, 993, "imap-a.example.net."), // lower weight
			srvRecord(10, 9, 993, "imap-z.example.net."), // higher weight: first
			srvRecord(20, 9, 993, "imap-b.example.net."), // higher priority number: last
		},
		"submissions": {srvRecord(0, 0, 465, "smtp.example.net.")},
	}}}
	want := []account.MailServer{
		smtpTLS(expected("imap-z.example.net", "smtp.example.net", testAddress)),
		smtpTLS(expected("imap-a.example.net", "smtp.example.net", testAddress)),
		smtpTLS(expected("imap-b.example.net", "smtp.example.net", testAddress)),
	}

	for shift := range 3 {
		dns.shift = shift

		got, err := newFinder(srv, dns).Mail(context.Background(), testAddress)

		require.NoError(t, err, "shift %d", shift)
		require.Equal(t, want, got, "shift %d", shift)
	}
}

func TestEqualMXRecordsGiveAStableGuess(t *testing.T) {
	srv, _ := newTLSServer(t, http.NewServeMux())
	// Three hosts with the same preference. Each has a different base domain,
	// so the order decides which base domain is guessed. The names have mixed
	// case on purpose: "MX.Beta..." would sort before "mx.alpha..." if the
	// order used the raw bytes.
	dns := &rotatingDNS{fakeDNS: &fakeDNS{mx: []*net.MX{
		{Host: "mx.zeta-mail.net.", Pref: 10},
		{Host: "mx.alpha-mail.org.", Pref: 10},
		{Host: "MX.Beta-mail.io.", Pref: 10},
		{Host: "mx.omega-mail.com.", Pref: 20}, // a worse preference never wins
	}}}
	want := []account.MailServer{expected("imap.alpha-mail.org", "smtp.alpha-mail.org", testAddress)}

	for shift := range 8 {
		dns.shift = shift

		got, err := newFinder(srv, dns).Mail(context.Background(), testAddress)

		require.NoError(t, err, "shift %d", shift)
		require.Equal(t, want, got, "shift %d", shift)
	}
}

// --- rooted DNS names ---

// A name with a dot at the end is a full name. The system resolver does not
// add its search domains to it, so a missing record is not looked up under
// another domain.
func TestDNSNamesAreRooted(t *testing.T) {
	srv, _ := newTLSServer(t, http.NewServeMux())
	dns := &fakeDNS{
		srv: map[string][]*net.SRV{
			"imaps":       {srvRecord(0, 0, 993, "imap.example.net.")},
			"submissions": {srvRecord(0, 0, 465, "smtp.example.net.")},
			"submission":  {srvRecord(0, 0, 587, "smtp.example.net.")},
		},
		mx: []*net.MX{{Host: "mx.mailhost.example.io.", Pref: 10}},
	}

	_, err := newFinder(srv, dns).Mail(context.Background(), testAddress)

	require.NoError(t, err)
	require.ElementsMatch(t, []string{
		"srv imaps tcp example.com.",
		"srv submissions tcp example.com.",
		"srv submission tcp example.com.",
		"mx example.com.",
	}, strings.Split(strings.TrimSpace(dns.allQueries()), "\n"))
}
