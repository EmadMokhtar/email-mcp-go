# Email MCP Server

[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![MCP](https://img.shields.io/badge/MCP-Compatible-green)](https://modelcontextprotocol.io/)

A Model Context Protocol (MCP) server that provides email capabilities through IMAP and SMTP protocols. This server enables AI assistants to interact with email accounts, read messages, send emails, and manage mailboxes.

**Quick Links:**
📚 [Quick Reference](QUICK_REFERENCE.md) | 🚀 [How It Works](HOW_IT_WORKS.md) | ⚙️ [Claude Setup](CLAUDE_SETUP.md) | 🐛 [Debugging](DEBUGGING.md)

## Features

### IMAP (Reading & Managing Emails)
- 📬 List mailboxes/folders
- 🔍 Search emails with advanced criteria (date, sender, subject, flags)
- 📧 Read full email content (text, HTML, attachments)
- ✅ Mark emails as read/unread
- 📁 Move/copy emails between folders
- 🗑️ Delete emails
- 📎 Download attachments

### SMTP (Sending Emails)
- ✉️ Send plain text and HTML emails
- 📎 Send emails with attachments
- 👥 CC and BCC support
- ↩️ Reply to emails
- ➡️ Forward emails

## Installation

### Prerequisites

- Go 1.22 or higher
- Email account with IMAP/SMTP access enabled
- For Gmail: [App-specific password](https://support.google.com/accounts/answer/185833) or OAuth2 credentials

### Install from Source

```bash
git clone https://github.com/EmadMokhtar/email-mcp-go.git
cd email-mcp-go
go build -o email-mcp ./cmd/email-mcp
```

### Install via Go

```bash
go install github.com/EmadMokhtar/email-mcp-go/cmd/email-mcp@latest
```

## Configuration

### Environment Variables

Create a `.env` file in the project root:

```bash
cp .env.example .env
```

Edit the `.env` file with your email credentials:

```env
# IMAP Configuration
IMAP_HOST=imap.gmail.com
IMAP_PORT=993
IMAP_USERNAME=your-email@gmail.com
IMAP_PASSWORD=your-app-password
IMAP_TLS=true

# SMTP Configuration
SMTP_HOST=smtp.gmail.com
SMTP_PORT=587
SMTP_USERNAME=your-email@gmail.com
SMTP_PASSWORD=your-app-password
SMTP_TLS=true
```

### Common Email Providers

#### Gmail
```env
IMAP_HOST=imap.gmail.com
IMAP_PORT=993
SMTP_HOST=smtp.gmail.com
SMTP_PORT=587
```
**Note:** Enable "Less secure app access" or use an [App Password](https://support.google.com/accounts/answer/185833)

#### Outlook/Office 365
```env
IMAP_HOST=outlook.office365.com
IMAP_PORT=993
SMTP_HOST=smtp.office365.com
SMTP_PORT=587
```

#### Yahoo Mail
```env
IMAP_HOST=imap.mail.yahoo.com
IMAP_PORT=993
SMTP_HOST=smtp.mail.yahoo.com
SMTP_PORT=587
```

#### ProtonMail Bridge
```env
IMAP_HOST=127.0.0.1
IMAP_PORT=1143
SMTP_HOST=127.0.0.1
SMTP_PORT=1025
IMAP_TLS=false
SMTP_TLS=false
```

## Usage

### Using with Claude Desktop

> **💡 You don't need to run the server manually!** Claude Desktop automatically starts and manages the MCP server. See [HOW_IT_WORKS.md](HOW_IT_WORKS.md) for details.

#### Quick Setup (Recommended)

1. Clone and configure:
```bash
git clone https://github.com/EmadMokhtar/email-mcp-go.git
cd email-mcp-go
cp .env.example .env
# Edit .env with your email credentials
```

2. Run the installer:
```bash
./install-claude.sh
```

3. Restart Claude Desktop

That's it! The email MCP server is now available in Claude. **No need to run anything manually!**

#### Manual Setup

See [CLAUDE_SETUP.md](CLAUDE_SETUP.md) for detailed manual configuration instructions.

#### Quick Manual Setup

1. Build the binary:
```bash
make build
```

2. Add to your Claude Desktop config (`~/Library/Application Support/Claude/claude_desktop_config.json` on macOS):
```json
{
  "mcpServers": {
    "email": {
      "command": "/absolute/path/to/email-mcp-go/bin/email-mcp",
      "env": {
        "IMAP_HOST": "imap.gmail.com",
        "IMAP_PORT": "993",
        "IMAP_USERNAME": "your-email@gmail.com",
        "IMAP_PASSWORD": "your-app-password",
        "IMAP_TLS": "true",
        "SMTP_HOST": "smtp.gmail.com",
        "SMTP_PORT": "587",
        "SMTP_USERNAME": "your-email@gmail.com",
        "SMTP_PASSWORD": "your-app-password",
        "SMTP_TLS": "true"
      }
    }
  }
}
```

3. Restart Claude Desktop

### Running the Server Standalone

```bash
# Using the binary
./bin/email-mcp

# Or with make
make run

# Or in development mode
make dev
```

## Available Tools

### `list_mailboxes`
Lists all available mailboxes/folders in the email account.

**Example prompt:**
```
Show me all my email folders
```

### `search_emails`
Search for emails based on various criteria.

**Parameters:**
- `from` - Filter by sender email
- `to` - Filter by recipient email
- `subject` - Filter by subject keywords
- `since` - Emails after this date (RFC3339)
- `before` - Emails before this date (RFC3339)
- `unseen` - Only unread emails (boolean)
- `folder` - Search in specific folder (default: "INBOX")
- `limit` - Maximum number of results (default: 50)

**Example prompts:**
```
Find all unread emails from john@example.com
Show me emails from the last week about "project alpha"
Search for emails in the Sent folder from yesterday
```

### `get_email`
Retrieve full email content by ID.

**Parameters:**
- `id` - Email sequence number
- `folder` - Mailbox name (default: "INBOX")
- `include_attachments` - Download attachments (boolean)

**Example prompt:**
```
Show me the full content of email ID 42
Get email 123 with attachments
```

### `send_email`
Send a new email.

**Parameters:**
- `to` - Recipient email addresses (array)
- `subject` - Email subject
- `body` - Email body content
- `is_html` - Send as HTML (boolean, default: false)
- `cc` - CC recipients (optional, array)
- `bcc` - BCC recipients (optional, array)
- `attachments` - File attachments (optional, array)

**Example prompts:**
```
Send an email to john@example.com with subject "Meeting" and body "Let's meet tomorrow"
Compose an HTML email to team@company.com about the quarterly report
```

### `reply_to_email`
Reply to an existing email.

**Parameters:**
- `email_id` - ID of email to reply to
- `body` - Reply message body
- `reply_all` - Reply to all recipients (boolean, default: false)
- `is_html` - Send as HTML (boolean, default: false)

**Example prompt:**
```
Reply to email 42 with "Thanks for the update!"
Reply all to the last email with confirmation
```

### `forward_email`
Forward an email to other recipients.

**Parameters:**
- `email_id` - ID of email to forward
- `to` - Forward recipients (array)
- `message` - Additional message (optional)

**Example prompt:**
```
Forward email 15 to sarah@example.com
```

### `mark_as_read` / `mark_as_unread`
Change read status of emails.

**Parameters:**
- `email_ids` - Array of email IDs
- `folder` - Mailbox name (default: "INBOX")

**Example prompts:**
```
Mark emails 1, 2, and 3 as read
Mark email 42 as unread
```

### `move_email`
Move email to a different folder.

**Parameters:**
- `email_id` - Email ID to move
- `from_folder` - Source folder
- `to_folder` - Destination folder

**Example prompt:**
```
Move email 42 from INBOX to Archive
```

### `delete_email`
Delete an email.

**Parameters:**
- `email_id` - Email ID to delete
- `folder` - Mailbox name (default: "INBOX")
- `permanent` - Permanently delete vs move to trash (boolean)

**Example prompt:**
```
Delete email 42 permanently
Move email 15 to trash
```

## Development

### Project Structure

```
email-mcp-go/
├── cmd/
│   └── email-mcp/        # Main application entry point
├── internal/
│   ├── server/           # MCP server implementation
│   ├── imap/             # IMAP client and operations
│   ├── smtp/             # SMTP client and operations
│   ├── config/           # Configuration management
│   └── tools/            # MCP tool definitions
├── pkg/
│   └── models/           # Data models
├── go.mod
├── go.sum
└── README.md
```

### Building

```bash
go build -o email-mcp ./cmd/email-mcp
```

### Testing

```bash
go test ./...
```

### Running in Development

```bash
go run ./cmd/email-mcp
```

## Security Considerations

⚠️ **Important Security Notes:**

1. **Credentials Storage**: Never commit credentials to version control. Always use environment variables or secure credential management.

2. **App Passwords**: For Gmail and other providers, use app-specific passwords instead of your main account password.

3. **OAuth2**: Consider implementing OAuth2 for production use with Gmail and Microsoft accounts.

4. **TLS/SSL**: Always use encrypted connections (TLS) for both IMAP and SMTP.

5. **Rate Limiting**: Be mindful of email provider rate limits to avoid account suspension.

6. **Permissions**: This MCP server has full access to your email account. Only use with trusted AI assistants.

## Troubleshooting

### Debugging

The Email MCP Server includes comprehensive logging to help debug issues. See **[DEBUGGING.md](DEBUGGING.md)** for detailed information about:

- Viewing logs in Claude Desktop
- Running the server standalone for debugging
- Understanding log messages and icons
- Common error patterns and solutions

**Quick Debug**: Run the server standalone to see all logs:
```bash
make run
# or
./bin/email-mcp
```

All logs are sent to stderr and include:
- 🚀 Startup and initialization
- 🔧 Tool calls and arguments
- ✅ Success messages
- ❌ Error details

### Common Issues

#### "Connection refused" or "Timeout"
- Check firewall settings
- Verify IMAP/SMTP ports are correct
- Ensure TLS settings match provider requirements

#### "Authentication failed"
- Verify username and password
- For Gmail: Enable "Less secure app access" or use App Password
- Check if 2FA is enabled and requires app-specific password

#### "Certificate error"
- Update Go to the latest version
- Check system certificates are up to date
- For self-signed certificates, you may need to disable TLS verification (not recommended for production)

#### Not seeing the server in Claude Desktop
- Check the configuration file path is correct
- Verify the binary path is absolute
- Restart Claude Desktop
- Check Claude Desktop logs: `~/Library/Logs/Claude/` (macOS)

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

1. Fork the repository
2. Create your feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add some amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## Acknowledgments

- Built with [mark3labs/mcp-go](https://github.com/mark3labs/mcp-go)
- Uses [emersion/go-imap](https://github.com/emersion/go-imap) for IMAP functionality
- Uses [go-mail/mail](https://github.com/go-mail/mail) for SMTP functionality
- Inspired by the [Model Context Protocol](https://modelcontextprotocol.io/) specification

## Related Projects

- [MCP Servers](https://github.com/modelcontextprotocol/servers) - Official MCP server implementations
- [Awesome MCP Servers](https://github.com/punkpeye/awesome-mcp-servers) - Curated list of MCP servers

## Support

For issues and questions:
- Open an issue on [GitHub](https://github.com/EmadMokhtar/email-mcp-go/issues)
- Check existing issues for solutions

## Roadmap

- [ ] OAuth2 support for Gmail and Microsoft
- [ ] Email templates
- [ ] Batch operations
- [ ] Email filtering rules
- [ ] Calendar integration (CalDAV)
- [ ] Contact management (CardDAV)
- [ ] Email scheduling
- [ ] S/MIME and PGP encryption support
- [ ] Webhook notifications for new emails

---

**Made with ❤️ by [Emad Mokhtar](https://github.com/EmadMokhtar)**