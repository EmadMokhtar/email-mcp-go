#!/bin/bash
# Simple Claude Desktop configuration script for Email MCP Server

set -e

echo "🔧 Configuring Email MCP Server for Claude Desktop"
echo "===================================================="
echo ""

# Detect OS
OS=$(uname -s)
if [ "$OS" = "Darwin" ]; then
    CONFIG_DIR="$HOME/Library/Application Support/Claude"
elif [ "$OS" = "Linux" ]; then
    CONFIG_DIR="$HOME/.config/Claude"
else
    echo "❌ Unsupported operating system: $OS"
    echo "Please manually edit your Claude Desktop configuration."
    echo "See CLAUDE_SETUP.md for instructions."
    exit 1
fi

CONFIG_FILE="$CONFIG_DIR/claude_desktop_config.json"

# Get absolute path to binary
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
BINARY_PATH="$SCRIPT_DIR/bin/email-mcp"

# Build the binary if it doesn't exist
if [ ! -f "$BINARY_PATH" ]; then
    echo "📦 Building email-mcp binary..."
    cd "$SCRIPT_DIR"
    make build
    if [ ! -f "$BINARY_PATH" ]; then
        echo "❌ Failed to build binary"
        exit 1
    fi
fi

echo "✅ Binary location: $BINARY_PATH"
echo ""

# Load environment variables from .env if it exists
if [ -f "$SCRIPT_DIR/.env" ]; then
    echo "📄 Loading configuration from .env file..."
    # Export variables from .env
    set -a
    source "$SCRIPT_DIR/.env"
    set +a
else
    echo "⚠️  No .env file found at $SCRIPT_DIR/.env"
    echo "Creating from example..."
    if [ -f "$SCRIPT_DIR/.env.example" ]; then
        cp "$SCRIPT_DIR/.env.example" "$SCRIPT_DIR/.env"
        echo "📝 Please edit $SCRIPT_DIR/.env with your email credentials"
        echo ""
    fi
    # Set defaults
    IMAP_HOST="${IMAP_HOST:-imap.gmail.com}"
    IMAP_PORT="${IMAP_PORT:-993}"
    IMAP_USERNAME="${IMAP_USERNAME:-}"
    IMAP_PASSWORD="${IMAP_PASSWORD:-}"
    IMAP_TLS="${IMAP_TLS:-true}"
    SMTP_HOST="${SMTP_HOST:-smtp.gmail.com}"
    SMTP_PORT="${SMTP_PORT:-587}"
    SMTP_USERNAME="${SMTP_USERNAME:-}"
    SMTP_PASSWORD="${SMTP_PASSWORD:-}"
    SMTP_TLS="${SMTP_TLS:-true}"
fi

# Create config directory
mkdir -p "$CONFIG_DIR"

# Backup existing config if it exists
if [ -f "$CONFIG_FILE" ]; then
    BACKUP_FILE="$CONFIG_FILE.backup.$(date +%Y%m%d_%H%M%S)"
    cp "$CONFIG_FILE" "$BACKUP_FILE"
    echo "📋 Backed up existing config to: $BACKUP_FILE"
fi

# Update only the "email" entry in the configuration.
#
# Writing the whole file would delete any other MCP servers the user has set
# up. Values are passed as arguments to a JSON encoder rather than pasted into
# a template, so a quote, backslash or newline in a path or password cannot
# produce a broken config file.
if [ -f "$CONFIG_FILE" ]; then
    EXISTING_CONFIG=$(cat "$CONFIG_FILE")
else
    EXISTING_CONFIG='{}'
fi

if command -v jq >/dev/null 2>&1; then
    printf '%s' "$EXISTING_CONFIG" | jq \
        --arg command "$BINARY_PATH" \
        --arg imap_host "$IMAP_HOST" \
        --arg imap_port "$IMAP_PORT" \
        --arg imap_username "$IMAP_USERNAME" \
        --arg imap_password "$IMAP_PASSWORD" \
        --arg imap_tls "$IMAP_TLS" \
        --arg smtp_host "$SMTP_HOST" \
        --arg smtp_port "$SMTP_PORT" \
        --arg smtp_username "$SMTP_USERNAME" \
        --arg smtp_password "$SMTP_PASSWORD" \
        --arg smtp_tls "$SMTP_TLS" \
        '.mcpServers.email = {
            command: $command,
            env: {
                IMAP_HOST: $imap_host,
                IMAP_PORT: $imap_port,
                IMAP_USERNAME: $imap_username,
                IMAP_PASSWORD: $imap_password,
                IMAP_TLS: $imap_tls,
                SMTP_HOST: $smtp_host,
                SMTP_PORT: $smtp_port,
                SMTP_USERNAME: $smtp_username,
                SMTP_PASSWORD: $smtp_password,
                SMTP_TLS: $smtp_tls
            }
        }' > "$CONFIG_FILE.tmp"
elif command -v python3 >/dev/null 2>&1; then
    EXISTING_CONFIG="$EXISTING_CONFIG" \
    BINARY_PATH="$BINARY_PATH" \
    IMAP_HOST="$IMAP_HOST" IMAP_PORT="$IMAP_PORT" \
    IMAP_USERNAME="$IMAP_USERNAME" IMAP_PASSWORD="$IMAP_PASSWORD" IMAP_TLS="$IMAP_TLS" \
    SMTP_HOST="$SMTP_HOST" SMTP_PORT="$SMTP_PORT" \
    SMTP_USERNAME="$SMTP_USERNAME" SMTP_PASSWORD="$SMTP_PASSWORD" SMTP_TLS="$SMTP_TLS" \
    python3 -c '
import json, os, sys

config = json.loads(os.environ["EXISTING_CONFIG"] or "{}")
config.setdefault("mcpServers", {})["email"] = {
    "command": os.environ["BINARY_PATH"],
    "env": {key: os.environ[key] for key in (
        "IMAP_HOST", "IMAP_PORT", "IMAP_USERNAME", "IMAP_PASSWORD", "IMAP_TLS",
        "SMTP_HOST", "SMTP_PORT", "SMTP_USERNAME", "SMTP_PASSWORD", "SMTP_TLS",
    )},
}
json.dump(config, sys.stdout, indent=2)
sys.stdout.write("\n")
' > "$CONFIG_FILE.tmp"
else
    echo "❌ Need either jq or python3 to update the configuration safely."
    echo "   Install one of them, or add the \"email\" server to $CONFIG_FILE by hand."
    exit 1
fi

# Only replace the real file once the new content was written successfully.
mv "$CONFIG_FILE.tmp" "$CONFIG_FILE"

echo "✅ Configuration written to: $CONFIG_FILE"
echo ""
echo "🎉 Installation Complete!"
echo ""
echo "📋 Configuration Summary:"
echo "   • Binary: $BINARY_PATH"
echo "   • IMAP Server: ${IMAP_HOST}:${IMAP_PORT}"
echo "   • SMTP Server: ${SMTP_HOST}:${SMTP_PORT}"
echo "   • Username: ${IMAP_USERNAME}"
echo ""
echo "Next steps:"
if [ -z "$IMAP_USERNAME" ] || [ -z "$IMAP_PASSWORD" ]; then
    echo "1. ⚠️  Edit $SCRIPT_DIR/.env with your email credentials"
    echo "2. Run this script again: ./install-claude.sh"
    echo "3. Restart Claude Desktop"
else
    echo "1. Restart Claude Desktop application"
    echo "2. Try asking Claude to 'list my email folders' or 'show my unread emails'"
fi
echo ""
echo "📖 For detailed setup instructions, see: CLAUDE_SETUP.md"
echo ""

