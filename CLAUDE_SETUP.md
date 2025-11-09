# Claude Desktop Configuration for Email MCP Server

This guide will help you configure the Email MCP Server to work with Claude Desktop.

> **📌 Important:** You do NOT need to manually run the server! Claude Desktop automatically starts, manages, and stops the MCP server for you. See [HOW_IT_WORKS.md](HOW_IT_WORKS.md) for details.

## Quick Setup (Automated)

Run the installation script:

```bash
./install-claude.sh
```

This will:
1. Build the email-mcp binary
2. Create/update your Claude Desktop configuration
3. Set up environment variables from your .env file
4. **That's it!** Just restart Claude Desktop and the server will automatically start.

## Manual Setup

### Step 1: Build the Binary

```bash
make build
```

This creates the binary at `./bin/email-mcp`

### Step 2: Configure Environment Variables

Create a `.env` file with your email credentials:

```bash
cp .env.example .env
```

Edit `.env` with your credentials:

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

### Step 3: Configure Claude Desktop

Edit your Claude Desktop configuration file:

**macOS**: `~/Library/Application Support/Claude/claude_desktop_config.json`

**Windows**: `%APPDATA%\Claude\claude_desktop_config.json`

**Linux**: `~/.config/Claude/claude_desktop_config.json`

Add or update the configuration:

```json
{
  "mcpServers": {
    "email": {
      "command": "/Users/emadmokhtar/Projects/email-mcp-go/bin/email-mcp",
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

**Important**: Replace the `command` path with the absolute path to your binary. To get it:

```bash
cd /Users/emadmokhtar/Projects/email-mcp-go
pwd
# Then append /bin/email-mcp to the output
```
1. Quit Claude Desktop completely (Cmd+Q on macOS)
### Step 4: Restart Claude Desktop
3. **The email MCP server will automatically start in the background!**
4. You don't need to run anything manually

> 💡 **Tip:** The server runs automatically when Claude Desktop is running. You'll never see it - it's background infrastructure. See [HOW_IT_WORKS.md](HOW_IT_WORKS.md) for details.
1. Quit Claude Desktop completely
2. Start Claude Desktop again
3. The email MCP server should now be available

## Verify Installation

In Claude Desktop, try asking:
- "List my email folders"
If Claude can answer these, the server is working! ✅

- "Show me my unread emails"
- "Search for emails from yesterday"

## Common Email Providers

### Gmail

```json
"IMAP_HOST": "imap.gmail.com",
"IMAP_PORT": "993",
"SMTP_HOST": "smtp.gmail.com",
"SMTP_PORT": "587"
```

**Note**: You need to create an [App Password](https://support.google.com/accounts/answer/185833) instead of using your regular password.

### Outlook/Office 365

```json
"IMAP_HOST": "outlook.office365.com",
"IMAP_PORT": "993",
"SMTP_HOST": "smtp.office365.com",
"SMTP_PORT": "587"
```

### Yahoo Mail

```json
"IMAP_HOST": "imap.mail.yahoo.com",
"IMAP_PORT": "993",
"SMTP_HOST": "smtp.mail.yahoo.com",
"SMTP_PORT": "587"
```

### iCloud Mail

```json
"IMAP_HOST": "imap.mail.me.com",
"IMAP_PORT": "993",
"SMTP_HOST": "smtp.mail.me.com",
"SMTP_PORT": "587"
```

**Note**: You need to create an [app-specific password](https://support.apple.com/en-us/HT204397).

## Troubleshooting

### Server Not Starting

1. Check Claude Desktop logs (usually in the app's log directory)
2. Verify the binary path in the config is correct and absolute
3. Ensure all environment variables are set correctly

### Authentication Errors

1. Verify your email credentials are correct
2. For Gmail, ensure you're using an App Password, not your regular password
3. Check if 2-factor authentication requires an app-specific password

### Connection Errors

1. Verify the IMAP/SMTP host and port settings
2. Check if your firewall is blocking connections
3. Ensure TLS settings match your provider's requirements

### Testing the Server Manually

You can test the server outside of Claude Desktop:

```bash
cd /Users/emadmokhtar/Projects/email-mcp-go
./bin/email-mcp
```

This will start the server in stdio mode. You can send MCP protocol messages to test it.

## Security Notes

1. **Never commit** your `.env` file or `claude_desktop_config.json` with real credentials
2. Use app-specific passwords when available (Gmail, iCloud, etc.)
3. Consider using environment variables or a secrets manager for production use
4. The credentials are stored in plain text in the Claude config file - ensure proper file permissions

## Updates

To update the MCP server:

```bash
git pull
make build
# Restart Claude Desktop
```

If you used the automated installer, run it again:

```bash
./install-claude.sh
```

