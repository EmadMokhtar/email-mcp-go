package main

// This file holds the "import-env" command. It moves the account of a .env file
// of the prototype (the old email-mcp, configured with IMAP_* and SMTP_*
// environment variables) into the database.
//
// The file is read with a small, strict parser (see parseEnvFile). Nothing of
// the file is printed except the address and, in a question about a
// certificate or a plain connection, the host and the port: the user must see
// which server they trust. An error names the variable, never its value.

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	netmail "net/mail"
	"os"
	"strconv"
	"strings"

	"github.com/EmadMokhtar/email-mcp-go/internal/connect"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
)

// maxEnvFileBytes is the largest .env file that import-env reads.
const maxEnvFileBytes = 64 << 10

// msgOAuth is the answer to a .env file that uses OAuth, exactly.
const msgOAuth = "This .env uses OAuth (USE_OAUTH=true). OAuth accounts are not supported until Google (M1) and " +
	"Microsoft (M2) support ships. Connect this account with an app password instead."

// The defaults of the prototype for the optional variables.
const (
	envDefaultIMAPPort = 993
	envDefaultSMTPPort = 587
)

func runImportEnv(ctx context.Context, c *cli, args []string) int {
	const cmd = "import-env"
	fs := c.newFlags(cmd)
	dataDir := fs.String("data-dir", "", dataDirUsage)
	email := fs.String("email", "", "the email address of the account (default: IMAP_USERNAME, when it is an address)")
	var trust fingerprintList
	fs.Var(&trust, "trust-fingerprint", "when standard input is not a terminal: trust the certificate with this SHA-256 "+
		"fingerprint, if a server shows it in this run (repeat the flag for the second server)")
	plaintext := fs.Bool("plaintext-i-understand", false, "allow IMAP_TLS=false or SMTP_TLS=false to a server on this computer")
	rest, code, ok := parseFlags(fs, args)
	if !ok {
		return code
	}
	if len(rest) > 1 {
		return c.usage(cmd, "give at most one path of a .env file")
	}
	path := ".env"
	if len(rest) == 1 {
		path = rest[0]
	}

	vars, err := readEnvFile(path)
	if err != nil {
		c.errorf("%s: %v\n", cmd, err)
		return exitError
	}
	// OAuth is refused before any other variable is read.
	if strings.EqualFold(vars["USE_OAUTH"], "true") {
		c.errorf("%s\n", msgOAuth)
		return exitError
	}
	imported, err := accountOfEnv(vars, path)
	if err != nil {
		c.errorf("%s: %v\n", cmd, err)
		return exitError
	}
	address := imported.address
	if *email != "" {
		address = *email
	} else if !plainAddress(address) {
		c.errorf("%s: IMAP_USERNAME is not an email address. Run again with --email and the address of the account.\n", cmd)
		return exitError
	}
	opts := connectOptions{trust: trust, plaintext: *plaintext}
	if !c.checkServer(cmd, imported.server, opts) {
		return exitError
	}

	a := c.openApp(ctx, cmd, *dataDir)
	if a == nil {
		return exitError
	}
	defer c.closeApp(ctx, cmd, a)
	acct, ok := c.saveAccount(userContext(ctx), cmd, a.Service.CreateIMAPAccount, address, imported.server, imported.password, opts)
	if !ok {
		return exitError
	}
	c.printf("Imported %s. Delete %s and remove the old \"env\" block from your harness config.\n", acct.Address, path)
	return exitOK
}

// envAccount is the account of a .env file.
type envAccount struct {
	address  string
	server   account.MailServer
	password secrets.Secret
}

// accountOfEnv reads the account from the variables of a .env file:
//   - IMAP_HOST, IMAP_USERNAME, IMAP_PASSWORD and SMTP_HOST are required;
//   - IMAP_PORT is 993 and SMTP_PORT is 587 when they are missing, as in the
//     prototype;
//   - IMAP_TLS and SMTP_TLS are true when they are missing. IMAP_TLS=true is TLS,
//     or STARTTLS on port 143. SMTP_TLS=true is STARTTLS, or TLS on port 465.
//     false is a plain connection;
//   - SMTP_USERNAME and SMTP_PASSWORD may be missing, or the same as the IMAP
//     ones: an account has one login for both servers.
//
// An empty value counts as missing. The errors name the variables, never their
// values.
func accountOfEnv(vars map[string]string, path string) (envAccount, error) {
	var missing []string
	for _, name := range []string{"IMAP_HOST", "IMAP_USERNAME", "IMAP_PASSWORD", "SMTP_HOST"} {
		if vars[name] == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return envAccount{}, fmt.Errorf("%s does not set %s. Nothing was saved", path, strings.Join(missing, ", "))
	}
	imapPort, err := envPort(vars, "IMAP_PORT", envDefaultIMAPPort)
	if err != nil {
		return envAccount{}, err
	}
	smtpPort, err := envPort(vars, "SMTP_PORT", envDefaultSMTPPort)
	if err != nil {
		return envAccount{}, err
	}
	imapTLS, err := envBool(vars, "IMAP_TLS")
	if err != nil {
		return envAccount{}, err
	}
	smtpTLS, err := envBool(vars, "SMTP_TLS")
	if err != nil {
		return envAccount{}, err
	}
	username, password := vars["IMAP_USERNAME"], vars["IMAP_PASSWORD"]
	if v := vars["SMTP_USERNAME"]; v != "" && v != username {
		return envAccount{}, errors.New("SMTP_USERNAME is not the same as IMAP_USERNAME. email-mcp logs in to both " +
			"servers with one user name and one password. Connect this account with \"email-mcp account add\" instead")
	}
	if v := vars["SMTP_PASSWORD"]; v != "" && subtle.ConstantTimeCompare([]byte(v), []byte(password)) != 1 {
		return envAccount{}, errors.New("SMTP_PASSWORD is not the same as IMAP_PASSWORD. email-mcp logs in to both " +
			"servers with one user name and one password. Connect this account with \"email-mcp account add\" instead")
	}

	imapSecurity, smtpSecurity := account.SecurityPlain, account.SecurityPlain
	if imapTLS {
		imapSecurity = account.SecurityTLS
		if imapPort == 143 {
			imapSecurity = account.SecuritySTARTTLS
		}
	}
	if smtpTLS {
		smtpSecurity = account.SecuritySTARTTLS
		if smtpPort == 465 {
			smtpSecurity = account.SecurityTLS
		}
	}
	return envAccount{
		address: username,
		server: account.MailServer{
			IMAPHost: connect.CleanHost(vars["IMAP_HOST"]), IMAPPort: imapPort, IMAPSecurity: imapSecurity,
			SMTPHost: connect.CleanHost(vars["SMTP_HOST"]), SMTPPort: smtpPort, SMTPSecurity: smtpSecurity,
			Username: username,
		},
		password: secrets.Secret(password),
	}, nil
}

// envPort reads a port variable. A missing one is def.
func envPort(vars map[string]string, name string, def int) (int, error) {
	text := vars[name]
	if text == "" {
		return def, nil
	}
	port, err := strconv.Atoi(text)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("%s is not a port number from 1 to 65535. Nothing was saved", name)
	}
	return port, nil
}

// envBool reads a true or false variable. A missing one is true, as in the
// prototype. Any other value is refused: the prototype took it as false, and a
// typing mistake must not turn the encryption off.
func envBool(vars map[string]string, name string) (bool, error) {
	switch strings.ToLower(vars[name]) {
	case "", "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be true or false. Nothing was saved", name)
	}
}

// plainAddress reports whether text is a plain email address, such as
// "alice@example.com", without a display name.
func plainAddress(text string) bool {
	parsed, err := netmail.ParseAddress(text)
	return err == nil && parsed.Name == "" && parsed.Address == text
}

// readEnvFile reads and parses a .env file of at most maxEnvFileBytes.
func readEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the user names the file to import
	if err != nil {
		return nil, fmt.Errorf("read the .env file: %w", err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxEnvFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read the .env file: %w", err)
	}
	return parseEnvFile(data)
}

// parseEnvFile parses the text of a .env file. It is strict:
//   - a line is empty, a comment (its first character that is not a space is
//     #), or NAME=VALUE;
//   - NAME is letters, digits and _, and does not start with a digit. The
//     spaces around NAME and VALUE go;
//   - a VALUE that starts with " or ' must end with the same quote. The text
//     between the quotes is kept as it is: no escapes, no comments;
//   - any other VALUE is kept as it is, also a # in it;
//   - a NAME that comes twice is refused.
//
// The errors give the number of the line, never its text: a line can hold a
// password.
func parseEnvFile(data []byte) (map[string]string, error) {
	if len(data) > maxEnvFileBytes {
		return nil, fmt.Errorf("the .env file is larger than %d bytes", maxEnvFileBytes)
	}
	vars := map[string]string{}
	for i, line := range strings.Split(string(data), "\n") {
		n := i + 1
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %d of the .env file is not NAME=VALUE", n)
		}
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !validEnvName(name) {
			return nil, fmt.Errorf("line %d of the .env file has a name that is not valid: use letters, digits and _", n)
		}
		if value != "" && (value[0] == '"' || value[0] == '\'') {
			if len(value) < 2 || value[len(value)-1] != value[0] {
				return nil, fmt.Errorf("line %d of the .env file has a quote that is not closed", n)
			}
			value = value[1 : len(value)-1]
		}
		if _, dup := vars[name]; dup {
			return nil, fmt.Errorf("line %d of the .env file sets %s a second time", n, name)
		}
		vars[name] = value
	}
	return vars, nil
}

// validEnvName reports whether name is letters, digits and _, and does not
// start with a digit.
func validEnvName(name string) bool {
	if name == "" || (name[0] >= '0' && name[0] <= '9') {
		return false
	}
	for i := 0; i < len(name); i++ {
		ch := name[i]
		if (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') && ch != '_' {
			return false
		}
	}
	return true
}
