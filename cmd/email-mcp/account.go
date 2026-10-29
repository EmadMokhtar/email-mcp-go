package main

// This file holds the "account" commands: add, list, remove and test. They are
// for a computer without a browser. The web interface does the same work.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"

	"github.com/EmadMokhtar/email-mcp-go/internal/connect"
	"github.com/EmadMokhtar/email-mcp-go/internal/discovery"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
)

// The usual ports of the security modes, used when a port flag is not given.
var (
	defaultIMAPPorts = map[account.Security]int{account.SecurityTLS: 993, account.SecuritySTARTTLS: 143, account.SecurityPlain: 143}
	defaultSMTPPorts = map[account.Security]int{account.SecurityTLS: 465, account.SecuritySTARTTLS: 587, account.SecurityPlain: 25}
)

// accountCommands are the subcommands of "account".
var accountCommands = []command{
	{name: "add", summary: "connect an IMAP and SMTP account (the password is read from standard input)", run: runAccountAdd},
	{name: "list", summary: "list the connected accounts", run: runAccountList},
	{name: "remove", summary: "remove an account: remove ID [--yes]", run: runAccountRemove},
	{name: "test", summary: "test the connection of an account: test ID", run: runAccountTest},
}

func runAccount(ctx context.Context, c *cli, args []string) int {
	return runGroup(ctx, c, "account", accountCommands, args)
}

// runGroup runs a subcommand of a group of commands, such as "account add".
func runGroup(ctx context.Context, c *cli, group string, commands []command, args []string) int {
	if len(args) == 0 {
		printGroupUsage(c.stderr, group, commands)
		return exitUsage
	}
	switch args[0] {
	case "help", "-h", "-help", "--help":
		printGroupUsage(c.stdout, group, commands)
		return exitOK
	}
	for _, cmd := range commands {
		if cmd.name == args[0] {
			return cmd.run(ctx, c, args[1:])
		}
	}
	c.errorf("%s: unknown command %q\n\n", group, args[0])
	printGroupUsage(c.stderr, group, commands)
	return exitUsage
}

// printGroupUsage writes the subcommands of a group to w.
func printGroupUsage(w io.Writer, group string, commands []command) {
	_, _ = fmt.Fprintf(w, "Usage:\n  email-mcp %s <command> [flags]\n\nCommands:\n", group)
	for _, cmd := range commands {
		_, _ = fmt.Fprintf(w, "  %-8s %s\n", cmd.name, cmd.summary)
	}
}

// serverFlags are the flags of "account add" that give the server settings.
type serverFlags struct {
	imapHost, smtpHost         *string
	imapPort, smtpPort         *int
	imapSecurity, smtpSecurity *string
}

func runAccountAdd(ctx context.Context, c *cli, args []string) int {
	const cmd = "account add"
	fs := c.newFlags(cmd)
	dataDir := fs.String("data-dir", "", dataDirUsage)
	email := fs.String("email", "", "the email address of the account (required)")
	sf := serverFlags{
		imapHost:     fs.String("imap-host", "", "the IMAP server, such as imap.example.com (give both hosts, or neither to look them up)"),
		imapPort:     fs.Int("imap-port", 0, "the IMAP port (default: 993 for tls, 143 for starttls and plain)"),
		imapSecurity: fs.String("imap-security", "tls", "the IMAP security: tls, starttls or plain"),
		smtpHost:     fs.String("smtp-host", "", "the SMTP server, such as smtp.example.com"),
		smtpPort:     fs.Int("smtp-port", 0, "the SMTP port (default: 465 for tls, 587 for starttls, 25 for plain)"),
		smtpSecurity: fs.String("smtp-security", "tls", "the SMTP security: tls, starttls or plain"),
	}
	username := fs.String("username", "", "the login name of both servers (default: the email address)")
	var trust fingerprintList
	fs.Var(&trust, "trust-fingerprint", "when standard input is not a terminal: trust the certificate with this SHA-256 "+
		"fingerprint, if a server shows it in this run (repeat the flag for the second server)")
	plaintext := fs.Bool("plaintext-i-understand", false, "allow a connection without encryption to a server on this computer")
	rest, code, ok := parseFlags(fs, args)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		return c.usage(cmd, "unexpected argument %q", rest[0])
	}
	if *email == "" {
		return c.usage(cmd, "--email is required")
	}
	server, code, ok := c.serverOf(cmd, fs, sf)
	if !ok {
		return code
	}
	if server.IMAPHost == "" {
		if server, ok = c.chooseDiscovered(ctx, cmd, *email); !ok {
			return exitError
		}
	}
	if *username != "" {
		server.Username = *username
	}
	opts := connectOptions{trust: trust, plaintext: *plaintext}
	if !c.checkServer(cmd, server, opts) {
		return exitError
	}

	password, err := c.readPassword(ctx, "Password (not shown): ")
	if err != nil {
		c.errorf("%s: %v\n", cmd, err)
		return exitError
	}
	if password == "" {
		c.errorf("%s: %s Nothing was saved.\n", cmd, connect.TextNoPassword)
		return exitError
	}

	a := c.openApp(ctx, cmd, *dataDir)
	if a == nil {
		return exitError
	}
	defer c.closeApp(ctx, cmd, a)
	acct, ok := c.saveAccount(userContext(ctx), cmd, a.Service.CreateIMAPAccount, *email, server, password, opts)
	if !ok {
		return exitError
	}
	c.printf("Connected %s. Its id is %s.\nMail level: %s. Sends and deletes ask for a confirmation.\n",
		acct.Address, acct.ID, acct.Level(account.AreaMail).MailName())
	return exitOK
}

// serverOf returns the settings of the flags, with cleaned hosts. Both hosts are
// empty when they must be looked up. It refuses one host without the other, a
// port or a security flag without the hosts, a security mode that is not known,
// and a port that is not 1 to 65535.
func (c *cli) serverOf(cmd string, fs *flag.FlagSet, sf serverFlags) (account.MailServer, int, bool) {
	server := account.MailServer{
		IMAPHost:     connect.CleanHost(*sf.imapHost),
		IMAPPort:     *sf.imapPort,
		IMAPSecurity: account.Security(*sf.imapSecurity),
		SMTPHost:     connect.CleanHost(*sf.smtpHost),
		SMTPPort:     *sf.smtpPort,
		SMTPSecurity: account.Security(*sf.smtpSecurity),
	}
	if (server.IMAPHost == "") != (server.SMTPHost == "") {
		return server, c.usage(cmd, "give both --imap-host and --smtp-host, or neither to look them up"), false
	}
	if server.IMAPHost == "" {
		var set string
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "imap-port", "smtp-port", "imap-security", "smtp-security":
				set = f.Name
			}
		})
		if set != "" {
			return server, c.usage(cmd, "--%s needs --imap-host and --smtp-host", set), false
		}
		return server, exitOK, true
	}
	for _, s := range []struct {
		flag string
		sec  account.Security
		port int
	}{
		{"imap", server.IMAPSecurity, server.IMAPPort},
		{"smtp", server.SMTPSecurity, server.SMTPPort},
	} {
		if s.sec != account.SecurityTLS && s.sec != account.SecuritySTARTTLS && s.sec != account.SecurityPlain {
			return server, c.usage(cmd, "--%s-security must be tls, starttls or plain", s.flag), false
		}
		if s.port < 0 || s.port > 65535 {
			return server, c.usage(cmd, "--%s-port must be 1 to 65535", s.flag), false
		}
	}
	if server.IMAPPort == 0 {
		server.IMAPPort = defaultIMAPPorts[server.IMAPSecurity]
	}
	if server.SMTPPort == 0 {
		server.SMTPPort = defaultSMTPPorts[server.SMTPSecurity]
	}
	return server, exitOK, true
}

// checkServer checks the settings before the password is read: the rules of a
// plain connection, then the checks of MailServer.Validate. The service checks
// again before it sends the password.
func (c *cli) checkServer(cmd string, server account.MailServer, opts connectOptions) bool {
	if !c.checkPlain(cmd, server, opts) {
		return false
	}
	if err := server.Validate(true); err != nil {
		c.errorf("%s: %s Nothing was saved.\n", cmd, connect.TextSettings)
		return false
	}
	return true
}

// chooseDiscovered looks up the settings of the address and lets the user choose
// them. Discovery only suggests: every suggested IMAP and SMTP host is shown, and
// the user confirms before any test sends the password. Without a terminal
// nobody can confirm, so it shows the suggestions and stops: the user must give
// --imap-host and --smtp-host.
func (c *cli) chooseDiscovered(ctx context.Context, cmd, address string) (account.MailServer, bool) {
	servers, err := c.sys.discover(ctx, address)
	switch {
	case errors.Is(err, discovery.ErrInvalidAddress):
		c.errorf("%s: %s\n", cmd, connect.TextAddress)
		return account.MailServer{}, false
	case err != nil || len(servers) == 0:
		c.errorf("%s: no server settings were found for this address. Give them with --imap-host and --smtp-host.\n", cmd)
		return account.MailServer{}, false
	}
	c.printf("Suggested server settings for %s:\n", address)
	for i, m := range servers {
		c.printf("  %d. IMAP %s:%d (%s), SMTP %s:%d (%s)\n", i+1,
			m.IMAPHost, m.IMAPPort, securityLabel(m.IMAPSecurity), m.SMTPHost, m.SMTPPort, securityLabel(m.SMTPSecurity))
	}
	if !c.sys.isTerminal() {
		c.errorf("%s: suggested settings need a confirmation, and without a terminal nobody can confirm them. "+
			"Run again with --imap-host and --smtp-host to choose the servers that get the password.\n", cmd)
		return account.MailServer{}, false
	}
	c.printf("The password goes only to the servers that you choose. Choose them only if you know these hosts.\n")
	choice := 1
	if len(servers) == 1 {
		if !c.confirm("Send the password to these servers?") {
			c.errorf("%s: stopped. Nothing was saved.\n", cmd)
			return account.MailServer{}, false
		}
	} else {
		answer, _ := c.ask(fmt.Sprintf("Choose the settings to use [1-%d], or press Enter to stop: ", len(servers)))
		n, err := strconv.Atoi(answer)
		if err != nil || n < 1 || n > len(servers) {
			c.errorf("%s: stopped. Nothing was saved.\n", cmd)
			return account.MailServer{}, false
		}
		choice = n
	}
	server := servers[choice-1]
	server.IMAPHost, server.SMTPHost = connect.CleanHost(server.IMAPHost), connect.CleanHost(server.SMTPHost)
	return server, true
}

// securityLabel returns the text of a security mode.
func securityLabel(s account.Security) string {
	switch s {
	case account.SecurityTLS:
		return "TLS"
	case account.SecuritySTARTTLS:
		return "STARTTLS"
	case account.SecurityPlain:
		return "not encrypted"
	default:
		return "unknown"
	}
}

// statusLabel returns the text of an account status.
func statusLabel(s account.Status) string {
	switch s {
	case account.StatusOK:
		return "connected"
	case account.StatusNeedsReauth:
		return "needs to be reconnected"
	default:
		return "unknown"
	}
}

func runAccountList(ctx context.Context, c *cli, args []string) int {
	const cmd = "account list"
	fs := c.newFlags(cmd)
	dataDir := fs.String("data-dir", "", dataDirUsage)
	rest, code, ok := parseFlags(fs, args)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		return c.usage(cmd, "unexpected argument %q", rest[0])
	}
	a := c.openApp(ctx, cmd, *dataDir)
	if a == nil {
		return exitError
	}
	defer c.closeApp(ctx, cmd, a)
	views, err := a.Service.ListStoredAccounts(userContext(ctx))
	if err != nil {
		c.errorf("%s: %v\n", cmd, err)
		return exitError
	}
	if len(views) == 0 {
		c.printf("No account is connected yet. Connect one with \"email-mcp account add\" or \"email-mcp setup\".\n")
		return exitOK
	}
	tw := tabwriter.NewWriter(c.stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tADDRESS\tMAIL LEVEL\tSTATUS")
	for _, v := range views {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", v.ID, v.Address, v.Areas[account.AreaMail], statusLabel(v.Status))
	}
	_ = tw.Flush()
	return exitOK
}

// findAccount returns the stored account with the id, or prints that there is
// none.
func (c *cli) findAccount(ctx context.Context, cmd string, svc *service.Service, id string) (service.AccountView, bool) {
	views, err := svc.ListStoredAccounts(userContext(ctx))
	if err != nil {
		c.errorf("%s: %v\n", cmd, err)
		return service.AccountView{}, false
	}
	for _, v := range views {
		if string(v.ID) == id {
			return v, true
		}
	}
	c.errorf("%s: No account has this id. \"email-mcp account list\" shows the ids.\n", cmd)
	return service.AccountView{}, false
}

// oneID reads the single argument of a command that takes an account id.
func (c *cli) oneID(cmd string, rest []string) (string, int, bool) {
	if len(rest) != 1 {
		return "", c.usage(cmd, "give one account id (\"email-mcp account list\" shows them)"), false
	}
	return rest[0], exitOK, true
}

func runAccountRemove(ctx context.Context, c *cli, args []string) int {
	const cmd = "account remove"
	fs := c.newFlags(cmd)
	dataDir := fs.String("data-dir", "", dataDirUsage)
	yes := fs.Bool("yes", false, "remove without a question (needed when standard input is not a terminal)")
	rest, code, ok := parseFlags(fs, args)
	if !ok {
		return code
	}
	id, code, ok := c.oneID(cmd, rest)
	if !ok {
		return code
	}
	a := c.openApp(ctx, cmd, *dataDir)
	if a == nil {
		return exitError
	}
	defer c.closeApp(ctx, cmd, a)
	view, ok := c.findAccount(ctx, cmd, a.Service, id)
	if !ok {
		return exitError
	}
	c.printf("Removing %s (%s) also deletes its queued messages that are not sent yet. They are never sent.\n", view.Address, view.ID)
	if !*yes {
		if !c.sys.isTerminal() {
			c.errorf("%s: without a terminal, run again with --yes to remove the account. Nothing was removed.\n", cmd)
			return exitError
		}
		if !c.confirm("Remove this account?") {
			c.errorf("%s: nothing was removed.\n", cmd)
			return exitError
		}
	}
	if err := a.Service.RemoveAccount(userContext(ctx), view.ID); err != nil {
		if errors.Is(err, service.ErrAccountNotFound) {
			c.errorf("%s: No account has this id.\n", cmd)
		} else {
			c.errorf("%s: %v\n", cmd, err)
		}
		return exitError
	}
	c.printf("Removed %s.\n", view.Address)
	return exitOK
}

func runAccountTest(ctx context.Context, c *cli, args []string) int {
	const cmd = "account test"
	fs := c.newFlags(cmd)
	dataDir := fs.String("data-dir", "", dataDirUsage)
	rest, code, ok := parseFlags(fs, args)
	if !ok {
		return code
	}
	id, code, ok := c.oneID(cmd, rest)
	if !ok {
		return code
	}
	a := c.openApp(ctx, cmd, *dataDir)
	if a == nil {
		return exitError
	}
	defer c.closeApp(ctx, cmd, a)
	view, ok := c.findAccount(ctx, cmd, a.Service, id)
	if !ok {
		return exitError
	}
	if err := a.Service.ProbeAccount(userContext(ctx), view.ID); err != nil {
		c.errorf("%s: %s\n", cmd, probeText(err))
		return exitError
	}
	c.printf("%s (%s): ok. The IMAP and the SMTP login work.\n", view.Address, view.ID)
	return exitOK
}

// probeText returns the text for an error of Service.ProbeAccount: a fixed
// text for a failed test, or the problem of each certificate with its
// fingerprint. It never holds the text of a server or the password.
func probeText(err error) string {
	var certErr *service.CertificateError
	if errors.As(err, &certErr) {
		text := ""
		for _, s := range sidesOf(&account.MailServer{}, certErr) {
			if s.cert == nil {
				continue
			}
			check := connect.CheckCertificate(s.cert)
			if text != "" {
				text += " "
			}
			if check.Fingerprint == "" {
				text += fmt.Sprintf("The %s certificate: %s", s.name, check.Problem)
			} else {
				text += fmt.Sprintf("The %s certificate (SHA-256 %s): %s", s.name, check.Fingerprint, check.Problem)
			}
		}
		return text
	}
	if errors.Is(err, service.ErrAccountNotFound) {
		return "No account has this id."
	}
	var testErr *service.ConnectionTestError
	if errors.As(err, &testErr) {
		return connect.TestFailureText(testErr.Err)
	}
	// Not a test of the servers: a failure of the database or of the master key.
	// Its text is ours, without the password.
	return "the account could not be tested: " + err.Error()
}
