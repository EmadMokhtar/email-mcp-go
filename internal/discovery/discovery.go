// Package discovery suggests IMAP and SMTP settings from an email address.
//
// A Finder asks four sources, in this order:
//
//  1. the Thunderbird ISP database (a public list of mail provider settings),
//  2. the config file that the domain itself may publish at
//     https://autoconfig.<domain>/mail/config-v1.1.xml,
//  3. DNS SRV records (_imaps._tcp, _submissions._tcp and _submission._tcp),
//  4. a guess from the MX record (imap.<base> and smtp.<base>).
//
// The result is only a list of suggestions. The caller must test a connection
// before it trusts any of them.
//
// The package keeps three promises:
//   - It never suggests an unencrypted connection. Every suggestion uses
//     implicit TLS or STARTTLS.
//   - It never sends the email address, or any part of it, to anyone. Only the
//     domain name goes into an HTTP request or a DNS query.
//   - It stays small and fast. It uses only https, reads at most 1 MiB from a
//     response, follows at most 3 redirects and stops after 10 seconds.
package discovery

import (
	"bytes"
	"cmp"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

// ErrNotFound means that no source gave a suggestion. When the lookup ended
// because the time ran out or the caller cancelled it, the error also wraps
// the context error.
var ErrNotFound = errors.New("discovery: no mail server settings found")

// ErrInvalidAddress means that the email address cannot be used for a lookup.
// The error text never contains the address.
var ErrInvalidAddress = errors.New("discovery: invalid email address")

const (
	// defaultISPDBBase is the Thunderbird ISP database. The domain is added to
	// the end of the path.
	defaultISPDBBase = "https://autoconfig.thunderbird.net/v1.1/"

	// defaultLookupTimeout is the time limit for one call of Mail.
	defaultLookupTimeout = 10 * time.Second
	// httpTimeout is the time limit for one request of the default HTTP client.
	httpTimeout = 10 * time.Second
	// maxRedirects is the number of redirects that one request may follow.
	maxRedirects = 3
	// maxBodyBytes is the most that is read from one HTTP response. A longer
	// response is skipped.
	maxBodyBytes = 1 << 20

	// maxServersPerSide limits how many IMAP servers and how many SMTP servers
	// one source may offer. This also limits the suggestions from one source.
	maxServersPerSide = 4
	// maxSuggestions limits the length of the result of Mail.
	maxSuggestions = 10

	// maxAddressBytes is the longest accepted email address (RFC 5321).
	maxAddressBytes = 254
	// maxHostBytes is the longest accepted host name in its ASCII form.
	maxHostBytes = 253
	// maxHostInputBytes is the longest host name that is converted. It stops
	// the conversion from working on very long text.
	maxHostInputBytes = 1024
	// maxLabelBytes is the longest label (part between dots) of a host name.
	maxLabelBytes = 63
)

// Resolver looks up DNS records. *net.Resolver implements it.
type Resolver interface {
	LookupSRV(ctx context.Context, service, proto, name string) (cname string, addrs []*net.SRV, err error)
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
}

// The standard resolver is the default one.
var _ Resolver = (*net.Resolver)(nil)

// Finder suggests mail server settings. The zero value works and uses the
// defaults that the field comments describe.
type Finder struct {
	// HTTP sends the requests. The default has a 10 second timeout. Whatever
	// client is used, a redirect to a URL that is not https is refused and at
	// most 3 redirects are followed.
	HTTP *http.Client

	// DNS looks up SRV and MX records. The default is net.DefaultResolver.
	DNS Resolver

	// ISPDBBase is the address of the Thunderbird ISP database. The default is
	// "https://autoconfig.thunderbird.net/v1.1/".
	ISPDBBase string

	// AutoconfigURL returns the address of the config file of a domain. The
	// domain is in ASCII form. The default is
	// "https://autoconfig.<domain>/mail/config-v1.1.xml". Tests use this to
	// point the finder at a local server.
	AutoconfigURL func(domain string) string

	// timeout replaces defaultLookupTimeout when it is set. Tests use it.
	timeout time.Duration
}

// address is an email address that was checked and split.
type address struct {
	full   string // the address as the user wrote it, without outer spaces
	local  string // the part before the last "@"
	domain string // the part after the last "@", as the user wrote it
	host   string // domain in lower case and ASCII form, safe for lookups
}

// Mail suggests settings for the email address.
//
// It asks all four sources at the same time, so that a slow source cannot use
// up the time of the others. The result keeps the order of the sources, has no
// duplicates and has at most 10 entries. Every entry is complete (IMAP and SMTP
// from the same source) and passes MailServer.Validate. The Username of an
// entry is the email address, unless the config file names another one.
//
// A source that fails, for any reason, gives nothing and the others still
// count. Mail returns an error only when no source gave a suggestion. That
// error wraps ErrNotFound. An address that cannot be used gives an error that
// wraps ErrInvalidAddress, before any network use.
func (f *Finder) Mail(ctx context.Context, email string) ([]account.MailServer, error) {
	addr, err := parseAddress(email)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, f.lookupTimeout())
	defer cancel()

	sources := []func(context.Context, address) []account.MailServer{
		f.fromISPDB,
		f.fromAutoconfig,
		f.fromSRV,
		f.fromMX,
	}
	results := make([][]account.MailServer, len(sources))
	var wg sync.WaitGroup
	for i, source := range sources {
		wg.Go(func() { results[i] = source(ctx, addr) })
	}
	wg.Wait()

	var suggestions []account.MailServer
	seen := make(map[account.MailServer]bool)
	for _, list := range results {
		for _, server := range list {
			if seen[server] || !acceptable(server) {
				continue
			}
			seen[server] = true
			suggestions = append(suggestions, server)
		}
	}
	if len(suggestions) == 0 {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrNotFound, err)
		}
		return nil, ErrNotFound
	}
	if len(suggestions) > maxSuggestions {
		suggestions = suggestions[:maxSuggestions]
	}
	return suggestions, nil
}

// acceptable reports whether a suggestion may be returned. The sources never
// build a plain suggestion. This check is a second guard.
func acceptable(s account.MailServer) bool {
	return encrypted(s.IMAPSecurity) && encrypted(s.SMTPSecurity) && s.Validate(true) == nil
}

func encrypted(s account.Security) bool {
	return s == account.SecurityTLS || s == account.SecuritySTARTTLS
}

func (f *Finder) lookupTimeout() time.Duration {
	if f.timeout > 0 {
		return f.timeout
	}
	return defaultLookupTimeout
}

func (f *Finder) dns() Resolver {
	if f.DNS != nil {
		return f.DNS
	}
	return net.DefaultResolver
}

// httpClient returns the client for the requests. A client from the caller is
// copied, so that the caller's client does not change. The copy checks every
// redirect with checkRedirect first and then with the caller's own check.
func (f *Finder) httpClient() *http.Client {
	if f.HTTP == nil {
		return &http.Client{Timeout: httpTimeout, CheckRedirect: checkRedirect}
	}
	client := *f.HTTP
	own := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := checkRedirect(req, via); err != nil {
			return err
		}
		if own != nil {
			return own(req, via)
		}
		return nil
	}
	return &client
}

// checkRedirect refuses a redirect to a URL that is not https and a fourth
// redirect. via holds the requests that were already sent.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if !isSecureURL(req.URL) {
		return errors.New("discovery: refusing a redirect to a URL that is not https")
	}
	if len(via) > maxRedirects {
		return errors.New("discovery: too many redirects")
	}
	return nil
}

// isSecureURL reports whether u is an https URL with a host and no user info.
func isSecureURL(u *url.URL) bool {
	return u.Scheme == "https" && u.Hostname() != "" && u.User == nil
}

func (f *Finder) ispdbURL(domain string) string {
	base := f.ISPDBBase
	if base == "" {
		base = defaultISPDBBase
	}
	u, err := url.JoinPath(base, domain)
	if err != nil {
		return ""
	}
	return u
}

func (f *Finder) autoconfigURL(domain string) string {
	if f.AutoconfigURL != nil {
		return f.AutoconfigURL(domain)
	}
	return "https://autoconfig." + domain + "/mail/config-v1.1.xml"
}

// --- the address ---

// parseAddress checks an email address and finds its domain.
//
// The domain is the part after the last "@". It is converted to lower case and
// to ASCII (punycode) with IDNA, and it must be a plain host name with at
// least two labels. Error messages never contain the address.
func parseAddress(email string) (address, error) {
	email = strings.TrimSpace(email)
	switch {
	case email == "":
		return address{}, invalidAddress("the address is empty")
	case len(email) > maxAddressBytes:
		return address{}, invalidAddress("the address is too long")
	}
	for _, r := range email {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return address{}, invalidAddress("the address has a space or a control character")
		}
	}
	at := strings.LastIndexByte(email, '@')
	switch {
	case at < 0:
		return address{}, invalidAddress("the address has no @")
	case at == 0:
		return address{}, invalidAddress("the part before @ is empty")
	}
	host, err := normalizeHost(email[at+1:])
	if err != nil {
		return address{}, invalidAddress("the domain is not a plain host name: " + err.Error())
	}
	return address{full: email, local: email[:at], domain: email[at+1:], host: host}, nil
}

func invalidAddress(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidAddress, reason)
}

// username returns the user name for a Thunderbird template. It replaces
// %EMAILADDRESS%, %EMAILLOCALPART% and %EMAILDOMAIN%. When there is no
// template, or the result is empty, too long, has a control character or still
// has a "%", it returns the email address.
func (a address) username(template string) string {
	template = strings.TrimSpace(template)
	if template == "" {
		return a.full
	}
	name := strings.NewReplacer(
		"%EMAILADDRESS%", a.full,
		"%EMAILLOCALPART%", a.local,
		"%EMAILDOMAIN%", a.domain,
	).Replace(template)
	if name == "" || len(name) > maxAddressBytes || strings.Contains(name, "%") ||
		strings.ContainsFunc(name, unicode.IsControl) {
		return a.full
	}
	return name
}

// --- host names ---

// normalizeHost converts a host name to lower case and ASCII form and checks
// it. It accepts only a plain DNS name: letters, digits and hyphens in labels
// that are separated by dots, with at least two labels. It rejects a scheme, a
// path, a port, a space, a user name, a "%" placeholder, a trailing dot, an IP
// address and a single name such as "localhost".
//
// The error text is a fixed phrase. It never contains the input.
func normalizeHost(name string) (string, error) {
	if name == "" {
		return "", errors.New("it is empty")
	}
	if len(name) > maxHostInputBytes {
		return "", errors.New("it is too long")
	}
	ascii, err := idna.Lookup.ToASCII(name)
	if err != nil {
		return "", errors.New("it has a character that is not allowed")
	}
	if err := checkASCIIHost(ascii); err != nil {
		return "", err
	}
	return ascii, nil
}

func checkASCIIHost(host string) error {
	if len(host) > maxHostBytes {
		return errors.New("it is too long")
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return errors.New("it needs at least two labels")
	}
	for _, label := range labels {
		switch {
		case label == "":
			return errors.New("it has an empty label")
		case len(label) > maxLabelBytes:
			return errors.New("it has a label that is too long")
		case label[0] == '-' || label[len(label)-1] == '-':
			return errors.New("it has a label that starts or ends with a hyphen")
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return errors.New("it has a character that is not allowed")
			}
		}
	}
	// A name that ends in a number is not a DNS name. This also rejects IPv4
	// addresses.
	if isDigits(labels[len(labels)-1]) {
		return errors.New("it looks like an IP address")
	}
	return nil
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

// --- servers and pairs ---

// endpoint is one server of one protocol, before it is paired.
type endpoint struct {
	host     string
	port     int
	security account.Security
	username string // used for the IMAP side only
}

// tlsFirst orders implicit TLS before STARTTLS.
func tlsFirst(a, b endpoint) int {
	return cmp.Compare(securityRank(a.security), securityRank(b.security))
}

func securityRank(s account.Security) int {
	if s == account.SecurityTLS {
		return 0
	}
	return 1
}

// limitServers keeps the first maxServersPerSide servers.
func limitServers(servers []endpoint) []endpoint {
	if len(servers) > maxServersPerSide {
		return servers[:maxServersPerSide]
	}
	return servers
}

// pair joins IMAP servers and SMTP servers in order: the first IMAP server
// with the first SMTP server, the second with the second, and so on. When one
// side has fewer servers, its last server is used again, so that every server
// appears in some pair. It returns nothing when a side is empty, because a
// suggestion must be complete.
func pair(imap, smtp []endpoint) []account.MailServer {
	if len(imap) == 0 || len(smtp) == 0 {
		return nil
	}
	count := max(len(imap), len(smtp))
	servers := make([]account.MailServer, 0, count)
	for i := range count {
		in := imap[min(i, len(imap)-1)]
		out := smtp[min(i, len(smtp)-1)]
		servers = append(servers, account.MailServer{
			IMAPHost:     in.host,
			IMAPPort:     in.port,
			IMAPSecurity: in.security,
			SMTPHost:     out.host,
			SMTPPort:     out.port,
			SMTPSecurity: out.security,
			Username:     in.username,
		})
	}
	return servers
}

// --- source 1 and 2: Thunderbird config files over https ---

func (f *Finder) fromISPDB(ctx context.Context, addr address) []account.MailServer {
	return f.fromConfigURL(ctx, f.ispdbURL(addr.host), addr)
}

func (f *Finder) fromAutoconfig(ctx context.Context, addr address) []account.MailServer {
	return f.fromConfigURL(ctx, f.autoconfigURL(addr.host), addr)
}

// fromConfigURL fetches and reads one config file. A failure gives nothing:
// the other sources are tried in any case.
func (f *Finder) fromConfigURL(ctx context.Context, rawURL string, addr address) []account.MailServer {
	body, err := f.fetch(ctx, rawURL)
	if err != nil {
		return nil
	}
	return parseConfig(body, addr)
}

var (
	errNotHTTPS = errors.New("discovery: only https URLs are fetched")
	errTooLarge = errors.New("discovery: the response is larger than 1 MiB")
)

// fetch gets an https URL. It reads at most 1 MiB and fails when the body is
// longer. It sends no cookies, no credentials and no data from the user.
func (f *Finder) fetch(ctx context.Context, rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || !isSecureURL(u) {
		return nil, errNotHTTPS
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/xml, text/xml;q=0.9, */*;q=0.1")
	resp, err := f.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discovery: unexpected HTTP status %d", resp.StatusCode)
	}
	// Read one byte more than the limit, to tell "exactly the limit" from
	// "longer than the limit".
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBodyBytes {
		return nil, errTooLarge
	}
	return body, nil
}

// The Thunderbird "autoconfig" format, version 1.1. Only the fields that are
// used are read. The decoder of encoding/xml does not load external entities.
type clientConfig struct {
	Providers []configProvider `xml:"emailProvider"`
}

type configProvider struct {
	Incoming []configServer `xml:"incomingServer"`
	Outgoing []configServer `xml:"outgoingServer"`
}

// configServer is one incomingServer or outgoingServer element. The port is
// text, so that one bad port does not make the whole file unreadable.
type configServer struct {
	Type       string `xml:"type,attr"`
	Hostname   string `xml:"hostname"`
	Port       string `xml:"port"`
	SocketType string `xml:"socketType"`
	Username   string `xml:"username"`
}

// parseConfig reads a Thunderbird config file. It uses the first provider
// element that gives at least one complete pair of IMAP and SMTP servers. It
// returns nothing when the XML is not valid.
func parseConfig(body []byte, addr address) []account.MailServer {
	var config clientConfig
	if err := xml.NewDecoder(bytes.NewReader(body)).Decode(&config); err != nil {
		return nil
	}
	for _, provider := range config.Providers {
		imap := configEndpoints(provider.Incoming, "imap", addr)
		smtp := configEndpoints(provider.Outgoing, "smtp", addr)
		if servers := pair(imap, smtp); len(servers) > 0 {
			return servers
		}
	}
	return nil
}

// configEndpoints returns the usable servers of one protocol, with implicit
// TLS first. A server is not usable when its type is another protocol, when its
// socket type is "plain" or unknown, when its host name is not a plain host
// name, or when its port is outside 1 to 65535.
func configEndpoints(servers []configServer, protocol string, addr address) []endpoint {
	var endpoints []endpoint
	for _, s := range servers {
		if e, ok := configEndpoint(s, protocol, addr); ok {
			endpoints = append(endpoints, e)
		}
	}
	slices.SortStableFunc(endpoints, tlsFirst)
	return limitServers(endpoints)
}

func configEndpoint(s configServer, protocol string, addr address) (endpoint, bool) {
	if !strings.EqualFold(strings.TrimSpace(s.Type), protocol) {
		return endpoint{}, false
	}
	var security account.Security
	switch strings.ToLower(strings.TrimSpace(s.SocketType)) {
	case "ssl":
		security = account.SecurityTLS
	case "starttls":
		security = account.SecuritySTARTTLS
	default:
		// "plain" is never suggested. An unknown value is not guessed.
		return endpoint{}, false
	}
	host, err := normalizeHost(strings.TrimSpace(s.Hostname))
	if err != nil {
		return endpoint{}, false
	}
	port, err := strconv.Atoi(strings.TrimSpace(s.Port))
	if err != nil || port < 1 || port > 65535 {
		return endpoint{}, false
	}
	return endpoint{host: host, port: port, security: security, username: addr.username(s.Username)}, true
}

// --- source 3: SRV records ---

// fromSRV reads the SRV records of RFC 6186. The IMAP side is _imaps._tcp
// (implicit TLS). The SMTP side is _submissions._tcp (implicit TLS) and then
// _submission._tcp (STARTTLS). The service _imap._tcp has no TLS from the first
// byte, so it is never used.
func (f *Finder) fromSRV(ctx context.Context, addr address) []account.MailServer {
	imap := f.srvEndpoints(ctx, "imaps", account.SecurityTLS, addr)
	if len(imap) == 0 {
		return nil
	}
	smtp := f.srvEndpoints(ctx, "submissions", account.SecurityTLS, addr)
	smtp = append(smtp, f.srvEndpoints(ctx, "submission", account.SecuritySTARTTLS, addr)...)
	return pair(imap, limitServers(smtp))
}

// srvEndpoints looks up one SRV service. Records come in order of priority
// (lowest number first) and, inside a priority, by weight (highest first). A
// target of "." means that the service is not offered.
func (f *Finder) srvEndpoints(ctx context.Context, service string, security account.Security, addr address) []endpoint {
	// The standard resolver can return good records together with an error,
	// when it has dropped bad records. So the error is not needed: no records
	// means nothing to use.
	_, records, _ := f.dns().LookupSRV(ctx, service, "tcp", addr.host)
	records = slices.DeleteFunc(slices.Clone(records), func(r *net.SRV) bool { return r == nil })
	slices.SortStableFunc(records, func(a, b *net.SRV) int {
		if c := cmp.Compare(a.Priority, b.Priority); c != 0 {
			return c
		}
		return cmp.Compare(b.Weight, a.Weight)
	})

	var endpoints []endpoint
	for _, r := range records {
		// DNS gives a full name with a dot at the end. A target of "." has
		// nothing left after the dot is removed.
		target := strings.TrimSuffix(strings.TrimSpace(r.Target), ".")
		if target == "" || r.Port == 0 {
			continue
		}
		host, err := normalizeHost(target)
		if err != nil {
			continue
		}
		endpoints = append(endpoints, endpoint{host: host, port: int(r.Port), security: security, username: addr.full})
	}
	return limitServers(endpoints)
}

// --- source 4: guess from the MX record ---

// fromMX guesses the servers from the mail exchanger of the domain. It takes
// the MX host with the lowest preference number, finds its registrable domain
// (for example "example.co.uk" for "mx1.mail.example.co.uk") and suggests
// imap.<base> on port 993 with TLS and smtp.<base> on port 587 with STARTTLS.
// It is a guess. A connection test must confirm it.
func (f *Finder) fromMX(ctx context.Context, addr address) []account.MailServer {
	// As for SRV, the error is not needed.
	records, _ := f.dns().LookupMX(ctx, addr.host)
	records = slices.DeleteFunc(slices.Clone(records), func(r *net.MX) bool { return r == nil })
	slices.SortStableFunc(records, func(a, b *net.MX) int { return cmp.Compare(a.Pref, b.Pref) })

	for _, r := range records {
		// An MX host of "." is a "null MX": the domain does not take mail.
		target := strings.TrimSuffix(strings.TrimSpace(r.Host), ".")
		if target == "" {
			continue
		}
		host, err := normalizeHost(target)
		if err != nil {
			continue
		}
		base, err := publicsuffix.EffectiveTLDPlusOne(host)
		if err != nil {
			continue
		}
		imapHost, err := normalizeHost("imap." + base)
		if err != nil {
			continue
		}
		smtpHost, err := normalizeHost("smtp." + base)
		if err != nil {
			continue
		}
		return []account.MailServer{{
			IMAPHost:     imapHost,
			IMAPPort:     993,
			IMAPSecurity: account.SecurityTLS,
			SMTPHost:     smtpHost,
			SMTPPort:     587,
			SMTPSecurity: account.SecuritySTARTTLS,
			Username:     addr.full,
		}}
	}
	return nil
}
