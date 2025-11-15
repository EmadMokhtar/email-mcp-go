package main

import (
	"context"
	"flag"
	"log"
	"os"

	"github.com/EmadMokhtar/email-mcp-go/internal/config"
	"github.com/EmadMokhtar/email-mcp-go/internal/server"
	"github.com/joho/godotenv"
)

func main() {
	// Parse command line flags
	httpMode := flag.Bool("http", false, "Run in HTTP mode instead of stdio mode")
	httpAddr := flag.String("addr", "localhost:8080", "HTTP server address (only used with -http)")
	flag.Parse()

	// Configure logging
	log.SetOutput(os.Stderr) // MCP servers should log to stderr
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	log.Println("========================================")
	log.Println("📧 Email MCP Server")
	log.Println("========================================")

	// Load environment variables
	log.Println("🔍 Loading environment variables...")
	if err := godotenv.Load(); err != nil {
		log.Println("⚠️  No .env file found, using system environment variables")
	} else {
		log.Println("✅ Loaded .env file")
	}

	// Load configuration
	log.Println("⚙️  Loading configuration...")
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("❌ Failed to load configuration: %v", err)
	}
	log.Printf("✅ Configuration loaded")
	log.Printf("   IMAP: %s@%s:%s (TLS: %v)", cfg.IMAPUsername, cfg.IMAPHost, cfg.IMAPPort, cfg.IMAPTLS)
	log.Printf("   SMTP: %s@%s:%s (TLS: %v)", cfg.SMTPUsername, cfg.SMTPHost, cfg.SMTPPort, cfg.SMTPTLS)
	log.Println("")

	// Create and start MCP server
	srv := server.NewEmailMCPServer(cfg)

	log.Println("")
	ctx := context.Background()

	if *httpMode {
		log.Printf("📡 Starting in HTTP mode on %s", *httpAddr)
		if err := srv.StartHTTP(ctx, *httpAddr); err != nil {
			log.Fatalf("❌ HTTP Server error: %v", err)
		}
	} else {
		log.Println("📡 Starting in stdio mode")
		if err := srv.Start(ctx); err != nil {
			log.Fatalf("❌ Server error: %v", err)
		}
	}
}
