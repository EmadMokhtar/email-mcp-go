# Email MCP Server - Quick Reference

## Do I Need to Run the Server?

**NO!** Claude Desktop runs it automatically. ✅

## Setup (One Time)

```bash
# 1. Configure credentials
cp .env.example .env
# Edit .env with your email settings

# 2. Install for Claude Desktop
./install-claude.sh

# 3. Restart Claude Desktop
# Quit (Cmd+Q) and start again
```

Done! 🎉

## How It Works

```
You open Claude Desktop
    ↓
Claude reads: ~/Library/Application Support/Claude/claude_desktop_config.json
    ↓
Claude automatically starts: /path/to/email-mcp
    ↓
Server runs in background (invisible to you)
    ↓
You ask Claude: "List my emails"
    ↓
Claude uses the email MCP server
    ↓
You get your emails! ✅
    ↓
You quit Claude Desktop
    ↓
Server automatically stops
```

## Available Commands in Claude

Just talk naturally to Claude:

- "List my email folders"
- "Show me unread emails"
- "Search for emails from john@example.com"
- "Get email ID 42"
- "Send an email to jane@example.com about the meeting"
- "Reply to email 123"
- "Forward email 456 to team@example.com"
- "Mark emails as read"
- "Move email to Archive"
- "Delete email 789"

## Debugging

### View logs (when Claude is running):
```bash
tail -f ~/Library/Logs/Claude/mcp*.log
```

### Test manually (for debugging):
```bash
make run
# or
./bin/email-mcp
```

### Check if server is running:
```bash
ps aux | grep email-mcp
```

## File Locations

| What | Where |
|------|-------|
| Binary | `/Users/emadmokhtar/Projects/email-mcp-go/bin/email-mcp` |
| Config (macOS) | `~/Library/Application Support/Claude/claude_desktop_config.json` |
| Logs (macOS) | `~/Library/Logs/Claude/mcp*.log` |
| Your credentials | `/Users/emadmokhtar/Projects/email-mcp-go/.env` |

## Common Issues

### Tools not showing up
- Restart Claude Desktop (Quit completely, then start)
- Check config file has correct binary path (must be absolute)
- Verify binary exists: `ls -la bin/email-mcp`

### Authentication errors
- Check credentials in .env file
- For Gmail: Use App Password, not regular password
- Verify IMAP/SMTP settings for your provider

### Can't see logs
```bash
# Create logs directory if needed
mkdir -p ~/Library/Logs/Claude

# Restart Claude Desktop
```

## Email Provider Settings

### Gmail
```env
IMAP_HOST=imap.gmail.com
IMAP_PORT=993
SMTP_HOST=smtp.gmail.com
SMTP_PORT=587
IMAP_TLS=true
SMTP_TLS=true
```
**Note:** Need [App Password](https://support.google.com/accounts/answer/185833)

### Outlook
```env
IMAP_HOST=outlook.office365.com
IMAP_PORT=993
SMTP_HOST=smtp.office365.com
SMTP_PORT=587
```

### Yahoo
```env
IMAP_HOST=imap.mail.yahoo.com
IMAP_PORT=993
SMTP_HOST=smtp.mail.yahoo.com
SMTP_PORT=587
```

## Development

```bash
# Build
make build

# Run standalone (for testing)
make run

# Format code
make fmt

# Run tests
make test

# Check everything
make check

# Clean build artifacts
make clean
```

## Documentation

- [HOW_IT_WORKS.md](HOW_IT_WORKS.md) - How Claude Desktop manages the server
- [CLAUDE_SETUP.md](CLAUDE_SETUP.md) - Detailed setup instructions
- [DEBUGGING.md](DEBUGGING.md) - Debugging guide and logs
- [README.md](README.md) - Full documentation

## Key Points

✅ Server runs **automatically** when Claude Desktop is open
✅ Logs go to `~/Library/Logs/Claude/` (not your terminal)
✅ Configure **once**, use forever
✅ No manual server management needed
✅ Restart Claude Desktop to reload config changes

❌ Don't run `./bin/email-mcp` manually (unless debugging)
❌ Don't need a terminal window open
❌ Don't need to manage the server process

## Getting Help

1. Check logs: `tail -f ~/Library/Logs/Claude/mcp*.log`
2. Run standalone to see errors: `make run`
3. Read [DEBUGGING.md](DEBUGGING.md)
4. Check [CLAUDE_SETUP.md](CLAUDE_SETUP.md)
5. File an issue on GitHub

---

**Remember:** The MCP server is invisible infrastructure. Once configured, it just works! 🚀

