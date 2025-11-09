# Email MCP Server - Debugging Guide

This guide explains how to debug the Email MCP Server using the comprehensive logging that has been added.

## Logging Overview

The Email MCP Server now includes detailed logging at every stage:

### 🚀 Startup Logging
- Environment variable loading
- Configuration validation
- IMAP/SMTP client initialization
- MCP server creation
- Tool registration

### 🔧 Tool Execution Logging
Each tool call logs:
- Tool name being called
- Arguments received
- Processing steps
- Success/failure status
- Results returned

### ❌ Error Logging
All errors are logged with:
- Clear error indicators (❌)
- Detailed error messages
- Context about what failed

## Viewing Logs

### When Running with Claude Desktop

Claude Desktop captures stderr output from MCP servers. To view the logs:

#### macOS
```bash
# View live logs
tail -f ~/Library/Logs/Claude/mcp*.log

# Or check the Claude logs directory
ls -la ~/Library/Logs/Claude/

# View specific server logs
cat ~/Library/Logs/Claude/mcp-server-email.log
```

#### Alternative: Check Console.app
1. Open **Console.app** (in /Applications/Utilities/)
2. Search for "email-mcp" or "MCP"
3. Filter by "Claude" process

### When Running Standalone

Run the server directly to see all logs in your terminal:

```bash
cd /Users/emadmokhtar/Projects/email-mcp-go

# Run with make
make run

# Or run the binary directly
./bin/email-mcp
```

You'll see output like:
```
========================================
📧 Email MCP Server
========================================
🔍 Loading environment variables...
✅ Loaded .env file
⚙️  Loading configuration...
✅ Configuration loaded
   IMAP: user@gmail.com@imap.gmail.com:993 (TLS: true)
   SMTP: user@gmail.com@smtp.gmail.com:587 (TLS: true)

🚀 Initializing Email MCP Server...
📧 Connecting to IMAP server: imap.gmail.com:993 (TLS: true)
✅ IMAP client initialized successfully
📤 Initializing SMTP client: smtp.gmail.com:587 (TLS: true)
✅ SMTP client initialized successfully
🔧 Creating MCP server (version 0.1.0)...
✅ MCP server created
🔨 Registering tools...
  📝 Registering tool: list_mailboxes
  📝 Registering tool: search_emails
  📝 Registering tool: get_email
  ... (more tools)
✅ All tools registered
✨ Email MCP Server initialization complete

========================================
🚀 Starting Email MCP Server (stdio mode)...
========================================
Server ready to accept MCP protocol messages
```

## Log Message Icons

The logs use emoji icons for quick visual scanning:

- 🚀 **Startup/Server** - Server initialization and starting
- ⚙️ **Configuration** - Configuration loading
- 📧 **IMAP** - IMAP operations
- 📤 **SMTP** - SMTP operations
- 🔧 **Tool Call** - Tool being executed
- 📝 **Registration** - Tool registration
- ✅ **Success** - Operation completed successfully
- ❌ **Error** - Operation failed
- ⚠️ **Warning** - Non-critical issue
- 🛑 **Shutdown** - Server stopping
- 👋 **Goodbye** - Clean exit

## Example Tool Call Logs

When a tool is called from Claude, you'll see:

```
🔧 Tool called: search_emails
   Arguments: map[folder:INBOX limit:10 unseen:true]
   Search criteria: {From: To: Subject: Since:0001-01-01 00:00:00 +0000 UTC Before:0001-01-01 00:00:00 +0000 UTC Unseen:true Folder:INBOX Limit:10}
✅ Found 3 emails matching criteria
```

## Debugging Common Issues

### Connection Issues

Look for these log entries:
```
❌ Failed to create IMAP client: dial tcp: connection refused
```

**Solution**: Check IMAP_HOST, IMAP_PORT, and firewall settings

### Authentication Issues

```
❌ Failed to create IMAP client: LOGIN failed
```

**Solution**: Verify IMAP_USERNAME and IMAP_PASSWORD. For Gmail, ensure you're using an App Password.

### Tool Execution Issues

```
🔧 Tool called: get_email
   Arguments: map[id:999 folder:INBOX]
❌ Failed to get email: message not found
```

**Solution**: The email ID doesn't exist in the specified folder.

### Argument Parsing Issues

```
🔧 Tool called: send_email
   Arguments: map[to:invalid subject:Test]
❌ Invalid arguments (unmarshal failed): json: cannot unmarshal string into Go struct field
```

**Solution**: Check the arguments being passed match the expected schema.

## Enabling More Verbose Logging

To add even more detailed logging, you can set the Go log flags:

Edit `cmd/email-mcp/main.go`:
```go
log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile | log.Llongfile)
```

This will include:
- Microsecond timestamps
- Full file paths
- Line numbers

## Log File Locations by Platform

### macOS
- Claude Desktop logs: `~/Library/Logs/Claude/`
- System logs: `/var/log/system.log` (search for "email-mcp")

### Linux
- Claude Desktop logs: `~/.config/Claude/logs/`
- System logs: `journalctl -f | grep email-mcp`

### Windows
- Claude Desktop logs: `%APPDATA%\Claude\Logs\`
- Event Viewer: Search for "email-mcp"

## Testing Logging

To test the logging without Claude Desktop:

1. **Start the server manually:**
   ```bash
   cd /Users/emadmokhtar/Projects/email-mcp-go
   ./bin/email-mcp 2>&1 | tee email-mcp.log
   ```

2. **Send test MCP messages** (requires the MCP protocol format)

3. **Check the output** in `email-mcp.log`

## Troubleshooting Tip

If you don't see logs in Claude Desktop:

1. **Ensure stderr is being captured** - All logs go to stderr
2. **Check Claude Desktop version** - Older versions may not log MCP servers
3. **Restart Claude Desktop** - After configuration changes
4. **Check file permissions** - Ensure the binary is executable

## Need More Help?

If you're still having issues:

1. Run the server standalone (see above)
2. Check the full error messages in the logs
3. Verify your .env configuration
4. Test IMAP/SMTP connectivity separately
5. Check the CLAUDE_SETUP.md for configuration help

## Advanced: Custom Log Output

You can redirect logs to a file when running standalone:

```bash
# Save logs to file
./bin/email-mcp 2>email-mcp-debug.log

# View logs in real-time while saving
./bin/email-mcp 2>&1 | tee email-mcp-debug.log

# Save only errors
./bin/email-mcp 2>errors.log 1>/dev/null
```

Remember: The MCP protocol communication happens on stdout, so logs MUST go to stderr to avoid interfering with the protocol.

