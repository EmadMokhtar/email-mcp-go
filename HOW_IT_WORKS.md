# How Claude Desktop Runs MCP Servers

## TL;DR
**You do NOT need to manually run the server.** Claude Desktop automatically starts, manages, and stops the MCP server for you.

## How It Works

### Automatic Server Management

When you configure an MCP server in Claude Desktop's config file:

```json
{
  "mcpServers": {
    "email": {
      "command": "/path/to/email-mcp",
      "env": { ... }
    }
  }
}
```

Claude Desktop will:

1. ✅ **Automatically start** the server when Claude Desktop launches
2. ✅ **Keep it running** in the background while you use Claude
3. ✅ **Communicate** with it via stdin/stdout (stdio protocol)
4. ✅ **Automatically restart** it if it crashes
5. ✅ **Automatically stop** it when you quit Claude Desktop

### You Don't Need To:
- ❌ Manually run `./bin/email-mcp`
- ❌ Keep a terminal window open
- ❌ Worry about starting/stopping the server
- ❌ Manage the server process

## Server Lifecycle

```
┌─────────────────────────────────────────┐
│  You Start Claude Desktop               │
└─────────────┬───────────────────────────┘
              │
              ▼
┌─────────────────────────────────────────┐
│  Claude Desktop reads config file       │
│  ~/Library/Application Support/Claude/  │
│  claude_desktop_config.json             │
└─────────────┬───────────────────────────┘
              │
              ▼
┌─────────────────────────────────────────┐
│  For each MCP server in config:         │
│  - Spawns process with "command"        │
│  - Sets environment variables           │
│  - Connects stdin/stdout                │
└─────────────┬───────────────────────────┘
              │
              ▼
┌─────────────────────────────────────────┐
│  Email MCP Server running!              │
│  - Logs go to stderr                    │
│  - MCP protocol on stdin/stdout         │
│  - Available to Claude                  │
└─────────────┬───────────────────────────┘
              │
              ▼
┌─────────────────────────────────────────┐
│  You use email tools in Claude          │
│  "List my email folders"                │
│  "Show unread emails"                   │
└─────────────┬───────────────────────────┘
              │
              ▼
┌─────────────────────────────────────────┐
│  You quit Claude Desktop                │
│  - Server automatically stops           │
└─────────────────────────────────────────┘
```

## Setup Steps (One Time Only)

### 1. Build the Binary
```bash
cd /Users/emadmokhtar/Projects/email-mcp-go
make build
```

### 2. Configure Claude Desktop
```bash
# Option A: Use the installer (recommended)
./install-claude.sh

# Option B: Manual configuration
# Edit: ~/Library/Application Support/Claude/claude_desktop_config.json
```

### 3. Restart Claude Desktop
- Quit Claude Desktop completely
- Start Claude Desktop again
- The server will automatically start!

### 4. Done!
Just use Claude normally. The email tools will be available.

## Verifying It's Working

### Check if the server is running:

When Claude Desktop is open, the server should be running:

```bash
# Check if the process is running
ps aux | grep email-mcp
```

You should see something like:
```
user  12345  ...  /Users/emadmokhtar/Projects/email-mcp-go/bin/email-mcp
```

### View the server logs:

```bash
# macOS
tail -f ~/Library/Logs/Claude/mcp*.log

# Or check all Claude logs
ls -la ~/Library/Logs/Claude/
```

### Test in Claude:

Just ask Claude:
- "List my email folders"
- "Show my unread emails"
- "Search for emails from yesterday"

If it works, the server is running correctly!

## When You WOULD Run It Manually

The only time you'd run the server manually is for **debugging**:

```bash
cd /Users/emadmokhtar/Projects/email-mcp-go

# Run to see all logs in your terminal
make run

# Or run directly
./bin/email-mcp
```

This is useful to:
- See detailed logs in real-time
- Debug connection issues
- Test before configuring in Claude Desktop
- Develop and test new features

**But for normal use with Claude Desktop, you never need to do this!**

## Troubleshooting

### Server not appearing in Claude

**Check the config file:**
```bash
cat ~/Library/Application\ Support/Claude/claude_desktop_config.json
```

Verify:
- The `command` path is **absolute** (starts with `/`)
- The binary file exists at that path
- The binary is executable (`chmod +x /path/to/email-mcp`)

**Restart Claude Desktop:**
- Quit completely (Cmd+Q)
- Wait a few seconds
- Start again

### Server starts but tools don't work

**Check the logs:**
```bash
tail -f ~/Library/Logs/Claude/mcp*.log
```

Look for:
- Connection errors (IMAP/SMTP)
- Authentication failures
- Missing environment variables

**Verify credentials:**
- Check your `.env` file has correct credentials
- Or check the `env` section in `claude_desktop_config.json`

### Server crashes or restarts

**Check logs for errors:**
```bash
grep -i error ~/Library/Logs/Claude/mcp*.log
```

Common issues:
- Invalid IMAP/SMTP credentials
- Network connectivity problems
- TLS/SSL certificate issues

**Test manually first:**
```bash
# Set environment variables
export IMAP_HOST=imap.gmail.com
export IMAP_PORT=993
# ... etc

# Run and see errors
./bin/email-mcp
```

## Key Takeaways

1. **Claude Desktop manages everything** - You don't run the server manually
2. **Server starts with Claude** - Automatically when you open Claude Desktop
3. **Server stops with Claude** - Automatically when you quit Claude Desktop
4. **Logs go to Claude's log directory** - Not your terminal
5. **Configure once, forget** - After initial setup, it just works

## Architecture Diagram

```
┌──────────────────────────────────────────────────┐
│  Claude Desktop Application                      │
│                                                   │
│  ┌────────────────────────────────────────────┐ │
│  │  MCP Server Manager                        │ │
│  │  - Reads claude_desktop_config.json        │ │
│  │  - Spawns server processes                 │ │
│  │  - Manages lifecycle                       │ │
│  └───────────┬────────────────────────────────┘ │
│              │ spawns                            │
│              ▼                                    │
│  ┌──────────────────────────────────┐           │
│  │  Email MCP Server Process        │           │
│  │  /path/to/email-mcp              │           │
│  │                                  │           │
│  │  stdin  ◄──── MCP Protocol ────┐│           │
│  │  stdout ─────► MCP Protocol ────┤│           │
│  │  stderr ─────► Logs ────────────┤│           │
│  └──────────────────────────────────┘│           │
└────────────────────────────────────┼─┼───────────┘
                                     │ │
                                     │ └──► ~/Library/Logs/Claude/
                                     │
                                     └────► Your email server (IMAP/SMTP)
```

## Summary

**For normal use:**
1. Configure once: `./install-claude.sh`
2. Restart Claude Desktop
3. Use email tools - that's it!

**For debugging:**
- Run manually: `make run`
- Check logs: `~/Library/Logs/Claude/`
- See [DEBUGGING.md](DEBUGGING.md) for more details

The MCP server is designed to be **invisible infrastructure** - it just works in the background!

