package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/EmadMokhtar/email-mcp-go/internal/config"
	"github.com/EmadMokhtar/email-mcp-go/internal/imap"
	"github.com/EmadMokhtar/email-mcp-go/internal/smtp"
	"github.com/EmadMokhtar/email-mcp-go/internal/tools"
	"github.com/EmadMokhtar/email-mcp-go/pkg/models"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/rs/cors"
)

type EmailMCPServer struct {
	// sseSessions maps a session id to its open event stream.
	sseSessions sync.Map
	mcpServer   *server.MCPServer
	imapClient  *imap.Client
	smtpClient  *smtp.Client
	config      *config.Config
}

func NewEmailMCPServer(cfg *config.Config) *EmailMCPServer {
	log.Println("🚀 Initializing Email MCP Server...")

	s := &EmailMCPServer{
		config: cfg,
	}

	// Initialize IMAP client (optional for testing)
	log.Printf("📧 Connecting to IMAP server: %s:%s (TLS: %v)", cfg.IMAPHost, cfg.IMAPPort, cfg.IMAPTLS)

	imapClient, err := imap.NewClient(cfg)
	if err != nil {
		log.Printf("⚠️ Failed to create IMAP client: %v (continuing anyway for testing)", err)
		// Don't fatal error - allow server to start for CORS testing
		s.imapClient = nil
	} else {
		s.imapClient = imapClient
		log.Println("✅ IMAP client initialized successfully")
	}

	// Initialize SMTP client
	log.Printf("📤 Initializing SMTP client: %s:%s (TLS: %v)", cfg.SMTPHost, cfg.SMTPPort, cfg.SMTPTLS)
	smtpClient := smtp.NewClient(cfg)
	s.smtpClient = smtpClient
	log.Println("✅ SMTP client initialized successfully")

	// Create MCP server
	log.Println("🔧 Creating MCP server (version 0.1.0)...")
	s.mcpServer = server.NewMCPServer(
		"email-mcp",
		"0.1.0",
	)
	log.Println("✅ MCP server created")

	// Register tools
	log.Println("🔨 Registering tools...")
	s.registerTools()
	log.Println("✅ All tools registered")

	log.Println("✨ Email MCP Server initialization complete")
	return s
}

func (s *EmailMCPServer) registerTools() {
	// List mailboxes tool
	log.Println("  📝 Registering tool: list_mailboxes")
	s.mcpServer.AddTool(
		tools.ListMailboxesTool(),
		s.handleListMailboxes,
	)

	// Search emails tool
	log.Println("  📝 Registering tool: search_emails")
	s.mcpServer.AddTool(
		tools.SearchEmailsTool(),
		s.handleSearchEmails,
	)

	// Get email tool
	log.Println("  📝 Registering tool: get_email")
	s.mcpServer.AddTool(
		tools.GetEmailTool(),
		s.handleGetEmail,
	)

	// Send email tool
	log.Println("  📝 Registering tool: send_email")
	s.mcpServer.AddTool(
		tools.SendEmailTool(),
		s.handleSendEmail,
	)

	// Reply to email tool
	log.Println("  📝 Registering tool: reply_to_email")
	s.mcpServer.AddTool(
		tools.ReplyToEmailTool(),
		s.handleReplyToEmail,
	)

	// Forward email tool
	log.Println("  📝 Registering tool: forward_email")
	s.mcpServer.AddTool(
		tools.ForwardEmailTool(),
		s.handleForwardEmail,
	)

	// Mark as read tool
	log.Println("  📝 Registering tool: mark_as_read")
	s.mcpServer.AddTool(
		tools.MarkAsReadTool(),
		s.handleMarkAsRead,
	)

	// Mark as unread tool
	log.Println("  📝 Registering tool: mark_as_unread")
	s.mcpServer.AddTool(
		tools.MarkAsUnreadTool(),
		s.handleMarkAsUnread,
	)

	// Move email tool
	log.Println("  📝 Registering tool: move_email")
	s.mcpServer.AddTool(
		tools.MoveEmailTool(),
		s.handleMoveEmail,
	)

	// Delete email tool
	log.Println("  📝 Registering tool: delete_email")
	s.mcpServer.AddTool(
		tools.DeleteEmailTool(),
		s.handleDeleteEmail,
	)
}

// loggableArgs lists the tool arguments that carry no private content and are
// therefore safe to write to the logs. Everything else - message bodies,
// subjects, recipients, attachments - is redacted, because these logs are
// plain files and, under Claude Desktop, are kept on disk.
var loggableArgs = map[string]bool{
	"folder":              true,
	"from_folder":         true,
	"to_folder":           true,
	"id":                  true,
	"email_id":            true,
	"email_ids":           true,
	"limit":               true,
	"permanent":           true,
	"reply_all":           true,
	"is_html":             true,
	"include_attachments": true,
	"seen":                true,
	"unseen":              true,
	"since":               true,
	"before":              true,
}

// argSummary renders tool arguments for the log, keeping the keys so a call can
// still be traced but replacing any value that could hold email content.
func argSummary(arguments map[string]interface{}) string {
	if len(arguments) == 0 {
		return "(none)"
	}

	keys := make([]string, 0, len(arguments))
	for k := range arguments {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		if loggableArgs[k] {
			parts = append(parts, fmt.Sprintf("%s=%v", k, arguments[k]))
			continue
		}
		parts = append(parts, k+"=<redacted>")
	}

	return strings.Join(parts, " ")
}

// requireIMAP returns the error result to send when there is no IMAP
// connection. The server deliberately starts without one so the HTTP layer can
// be exercised, so every IMAP-backed handler has to check before using it.
func (s *EmailMCPServer) requireIMAP() *mcp.CallToolResult {
	if s.imapClient == nil {
		return mcp.NewToolResultError("IMAP client not initialized - server running in testing mode")
	}

	return nil
}

func (s *EmailMCPServer) handleListMailboxes(arguments map[string]interface{}) (*mcp.CallToolResult, error) {
	log.Println("🔧 Tool called: list_mailboxes")
	log.Printf("   Arguments: %s", argSummary(arguments))

	if res := s.requireIMAP(); res != nil {
		return res, nil
	}

	mailboxes, err := s.imapClient.ListMailboxes()
	if err != nil {
		log.Printf("❌ Failed to list mailboxes: %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Failed to list mailboxes: %v", err)), nil
	}

	log.Printf("✅ Found %d mailboxes", len(mailboxes))
	result, err := json.Marshal(mailboxes)
	if err != nil {
		log.Printf("❌ Failed to marshal result: %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Failed to marshal result: %v", err)), nil
	}

	return mcp.NewToolResultText(string(result)), nil
}

func (s *EmailMCPServer) handleSearchEmails(arguments map[string]interface{}) (*mcp.CallToolResult, error) {
	log.Println("🔧 Tool called: search_emails")
	log.Printf("   Arguments: %s", argSummary(arguments))

	if res := s.requireIMAP(); res != nil {
		return res, nil
	}

	var criteria models.SearchCriteria

	// Convert arguments to JSON and unmarshal
	argBytes, err := json.Marshal(arguments)
	if err != nil {
		log.Printf("❌ Invalid arguments (marshal failed): %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Invalid arguments: %v", err)), nil
	}

	if err := json.Unmarshal(argBytes, &criteria); err != nil {
		log.Printf("❌ Invalid arguments (unmarshal failed): %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Invalid arguments: %v", err)), nil
	}

	log.Printf("   Search criteria: folder=%q limit=%d unseen=%v seen=%v", criteria.Folder, criteria.Limit, criteria.Unseen, criteria.Seen)
	emails, err := s.imapClient.SearchEmails(&criteria)
	if err != nil {
		log.Printf("❌ Failed to search emails: %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Failed to search emails: %v", err)), nil
	}

	log.Printf("✅ Found %d emails matching criteria", len(emails))
	result, err := json.Marshal(emails)
	if err != nil {
		log.Printf("❌ Failed to marshal result: %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Failed to marshal result: %v", err)), nil
	}

	return mcp.NewToolResultText(string(result)), nil
}

func (s *EmailMCPServer) handleGetEmail(arguments map[string]interface{}) (*mcp.CallToolResult, error) {
	log.Println("🔧 Tool called: get_email")
	log.Printf("   Arguments: %s", argSummary(arguments))

	if res := s.requireIMAP(); res != nil {
		return res, nil
	}

	var params struct {
		ID                 uint32 `json:"id"`
		Folder             string `json:"folder"`
		IncludeAttachments bool   `json:"include_attachments"`
	}

	// Convert arguments to JSON and unmarshal
	argBytes, err := json.Marshal(arguments)
	if err != nil {
		log.Printf("❌ Invalid arguments (marshal failed): %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Invalid arguments: %v", err)), nil
	}

	if err := json.Unmarshal(argBytes, &params); err != nil {
		log.Printf("❌ Invalid arguments (unmarshal failed): %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Invalid arguments: %v", err)), nil
	}

	if params.Folder == "" {
		params.Folder = "INBOX"
	}

	log.Printf("   Getting email ID %d from folder '%s' (attachments: %v)", params.ID, params.Folder, params.IncludeAttachments)
	email, err := s.imapClient.GetEmail(params.ID, params.Folder, params.IncludeAttachments)
	if err != nil {
		log.Printf("❌ Failed to get email: %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Failed to get email: %v", err)), nil
	}

	log.Printf("✅ Retrieved email uid=%d (%d attachment(s), %d bytes)", email.ID, len(email.Attachments), email.Size)
	result, err := json.Marshal(email)
	if err != nil {
		log.Printf("❌ Failed to marshal result: %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Failed to marshal result: %v", err)), nil
	}

	return mcp.NewToolResultText(string(result)), nil
}

func (s *EmailMCPServer) handleSendEmail(arguments map[string]interface{}) (*mcp.CallToolResult, error) {
	log.Println("🔧 Tool called: send_email")
	log.Printf("   Arguments: %s", argSummary(arguments))

	var emailReq models.SendEmailRequest

	// Convert arguments to JSON and unmarshal
	argBytes, err := json.Marshal(arguments)
	if err != nil {
		log.Printf("❌ Invalid arguments (marshal failed): %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Invalid arguments: %v", err)), nil
	}

	if err := json.Unmarshal(argBytes, &emailReq); err != nil {
		log.Printf("❌ Invalid arguments (unmarshal failed): %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Invalid arguments: %v", err)), nil
	}

	log.Printf("   Sending email to %d recipient(s), %d attachment(s)", len(emailReq.To)+len(emailReq.Cc)+len(emailReq.Bcc), len(emailReq.Attachments))
	if err := s.smtpClient.SendEmail(&emailReq); err != nil {
		log.Printf("❌ Failed to send email: %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Failed to send email: %v", err)), nil
	}

	log.Println("✅ Email sent successfully")
	return mcp.NewToolResultText("Email sent successfully"), nil
}

func (s *EmailMCPServer) handleReplyToEmail(arguments map[string]interface{}) (*mcp.CallToolResult, error) {
	log.Println("🔧 Tool called: reply_to_email")
	log.Printf("   Arguments: %s", argSummary(arguments))

	if res := s.requireIMAP(); res != nil {
		return res, nil
	}

	var params struct {
		EmailID  uint32 `json:"email_id"`
		Folder   string `json:"folder"`
		Body     string `json:"body"`
		ReplyAll bool   `json:"reply_all"`
		IsHTML   bool   `json:"is_html"`
	}

	// Convert arguments to JSON and unmarshal
	argBytes, err := json.Marshal(arguments)
	if err != nil {
		log.Printf("❌ Invalid arguments (marshal failed): %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Invalid arguments: %v", err)), nil
	}

	if err := json.Unmarshal(argBytes, &params); err != nil {
		log.Printf("❌ Invalid arguments (unmarshal failed): %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Invalid arguments: %v", err)), nil
	}

	if params.Folder == "" {
		params.Folder = "INBOX"
	}

	log.Printf("   Replying to email ID %d from folder '%s' (reply_all: %v)", params.EmailID, params.Folder, params.ReplyAll)
	// Get original email
	originalEmail, err := s.imapClient.GetEmail(params.EmailID, params.Folder, false)
	if err != nil {
		log.Printf("❌ Failed to get original email: %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Failed to get original email: %v", err)), nil
	}

	// Create reply
	if err := s.smtpClient.ReplyToEmail(originalEmail, params.Body, params.ReplyAll, params.IsHTML); err != nil {
		log.Printf("❌ Failed to send reply: %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Failed to send reply: %v", err)), nil
	}

	log.Println("✅ Reply sent successfully")
	return mcp.NewToolResultText("Reply sent successfully"), nil
}

func (s *EmailMCPServer) handleForwardEmail(arguments map[string]interface{}) (*mcp.CallToolResult, error) {
	log.Println("🔧 Tool called: forward_email")
	log.Printf("   Arguments: %s", argSummary(arguments))

	if res := s.requireIMAP(); res != nil {
		return res, nil
	}

	var params struct {
		EmailID uint32   `json:"email_id"`
		Folder  string   `json:"folder"`
		To      []string `json:"to"`
		Message string   `json:"message"`
	}

	// Convert arguments to JSON and unmarshal
	argBytes, err := json.Marshal(arguments)
	if err != nil {
		log.Printf("❌ Invalid arguments (marshal failed): %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Invalid arguments: %v", err)), nil
	}

	if err := json.Unmarshal(argBytes, &params); err != nil {
		log.Printf("❌ Invalid arguments (unmarshal failed): %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Invalid arguments: %v", err)), nil
	}

	if params.Folder == "" {
		params.Folder = "INBOX"
	}

	log.Printf("   Forwarding email uid=%d from folder '%s' to %d recipient(s)", params.EmailID, params.Folder, len(params.To))
	// Get original email
	originalEmail, err := s.imapClient.GetEmail(params.EmailID, params.Folder, true)
	if err != nil {
		log.Printf("❌ Failed to get original email: %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Failed to get original email: %v", err)), nil
	}

	// Forward email
	if err := s.smtpClient.ForwardEmail(originalEmail, params.To, params.Message); err != nil {
		log.Printf("❌ Failed to forward email: %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Failed to forward email: %v", err)), nil
	}

	log.Println("✅ Email forwarded successfully")
	return mcp.NewToolResultText("Email forwarded successfully"), nil
}

func (s *EmailMCPServer) handleMarkAsRead(arguments map[string]interface{}) (*mcp.CallToolResult, error) {
	log.Println("🔧 Tool called: mark_as_read")
	log.Printf("   Arguments: %s", argSummary(arguments))

	if res := s.requireIMAP(); res != nil {
		return res, nil
	}

	var params struct {
		EmailIDs []uint32 `json:"email_ids"`
		Folder   string   `json:"folder"`
	}

	// Convert arguments to JSON and unmarshal
	argBytes, err := json.Marshal(arguments)
	if err != nil {
		log.Printf("❌ Invalid arguments (marshal failed): %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Invalid arguments: %v", err)), nil
	}

	if err := json.Unmarshal(argBytes, &params); err != nil {
		log.Printf("❌ Invalid arguments (unmarshal failed): %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Invalid arguments: %v", err)), nil
	}

	if params.Folder == "" {
		params.Folder = "INBOX"
	}

	log.Printf("   Marking %d email(s) as read in folder '%s'", len(params.EmailIDs), params.Folder)
	if err := s.imapClient.MarkAsRead(params.EmailIDs, params.Folder); err != nil {
		log.Printf("❌ Failed to mark as read: %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Failed to mark as read: %v", err)), nil
	}

	log.Println("✅ Emails marked as read")
	return mcp.NewToolResultText("Emails marked as read"), nil
}

func (s *EmailMCPServer) handleMarkAsUnread(arguments map[string]interface{}) (*mcp.CallToolResult, error) {
	log.Println("🔧 Tool called: mark_as_unread")
	log.Printf("   Arguments: %s", argSummary(arguments))

	if res := s.requireIMAP(); res != nil {
		return res, nil
	}

	var params struct {
		EmailIDs []uint32 `json:"email_ids"`
		Folder   string   `json:"folder"`
	}

	// Convert arguments to JSON and unmarshal
	argBytes, err := json.Marshal(arguments)
	if err != nil {
		log.Printf("❌ Invalid arguments (marshal failed): %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Invalid arguments: %v", err)), nil
	}

	if err := json.Unmarshal(argBytes, &params); err != nil {
		log.Printf("❌ Invalid arguments (unmarshal failed): %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Invalid arguments: %v", err)), nil
	}

	if params.Folder == "" {
		params.Folder = "INBOX"
	}

	log.Printf("   Marking %d email(s) as unread in folder '%s'", len(params.EmailIDs), params.Folder)
	if err := s.imapClient.MarkAsUnread(params.EmailIDs, params.Folder); err != nil {
		log.Printf("❌ Failed to mark as unread: %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Failed to mark as unread: %v", err)), nil
	}

	log.Println("✅ Emails marked as unread")
	return mcp.NewToolResultText("Emails marked as unread"), nil
}

func (s *EmailMCPServer) handleMoveEmail(arguments map[string]interface{}) (*mcp.CallToolResult, error) {
	log.Println("🔧 Tool called: move_email")
	log.Printf("   Arguments: %s", argSummary(arguments))

	if res := s.requireIMAP(); res != nil {
		return res, nil
	}

	var params struct {
		EmailID    uint32 `json:"email_id"`
		FromFolder string `json:"from_folder"`
		ToFolder   string `json:"to_folder"`
	}

	// Convert arguments to JSON and unmarshal
	argBytes, err := json.Marshal(arguments)
	if err != nil {
		log.Printf("❌ Invalid arguments (marshal failed): %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Invalid arguments: %v", err)), nil
	}

	if err := json.Unmarshal(argBytes, &params); err != nil {
		log.Printf("❌ Invalid arguments (unmarshal failed): %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Invalid arguments: %v", err)), nil
	}

	if params.FromFolder == "" {
		params.FromFolder = "INBOX"
	}

	log.Printf("   Moving email ID %d from '%s' to '%s'", params.EmailID, params.FromFolder, params.ToFolder)
	if err := s.imapClient.MoveEmail(params.EmailID, params.FromFolder, params.ToFolder); err != nil {
		log.Printf("❌ Failed to move email: %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Failed to move email: %v", err)), nil
	}

	log.Printf("✅ Email moved from %s to %s", params.FromFolder, params.ToFolder)
	return mcp.NewToolResultText(fmt.Sprintf("Email moved from %s to %s", params.FromFolder, params.ToFolder)), nil
}

func (s *EmailMCPServer) handleDeleteEmail(arguments map[string]interface{}) (*mcp.CallToolResult, error) {
	log.Println("🔧 Tool called: delete_email")
	log.Printf("   Arguments: %s", argSummary(arguments))

	if res := s.requireIMAP(); res != nil {
		return res, nil
	}

	var params struct {
		EmailID   uint32 `json:"email_id"`
		Folder    string `json:"folder"`
		Permanent bool   `json:"permanent"`
	}

	// Convert arguments to JSON and unmarshal
	argBytes, err := json.Marshal(arguments)
	if err != nil {
		log.Printf("❌ Invalid arguments (marshal failed): %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Invalid arguments: %v", err)), nil
	}

	if err := json.Unmarshal(argBytes, &params); err != nil {
		log.Printf("❌ Invalid arguments (unmarshal failed): %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Invalid arguments: %v", err)), nil
	}

	if params.Folder == "" {
		params.Folder = "INBOX"
	}

	log.Printf("   Deleting email ID %d from folder '%s' (permanent: %v)", params.EmailID, params.Folder, params.Permanent)
	if err := s.imapClient.DeleteEmail(params.EmailID, params.Folder, params.Permanent); err != nil {
		log.Printf("❌ Failed to delete email: %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Failed to delete email: %v", err)), nil
	}

	log.Println("✅ Email deleted successfully")
	return mcp.NewToolResultText("Email deleted successfully"), nil
}

func (s *EmailMCPServer) Start(ctx context.Context) error {
	log.Println("========================================")
	log.Println("🚀 Starting Email MCP Server (stdio mode)...")
	log.Println("========================================")
	log.Printf("Server ready to accept MCP protocol messages")
	log.Println("")

	// Create stdio server
	stdioServer := server.NewStdioServer(s.mcpServer)

	// Listen on stdin/stdout
	if err := stdioServer.Listen(ctx, os.Stdin, os.Stdout); err != nil {
		log.Printf("❌ Server error: %v", err)
		return err
	}

	return nil
}

// requireAuth rejects requests that do not carry the configured bearer token.
// Preflight requests pass through, because a browser never attaches
// credentials to them; the real request that follows is still checked.
func (s *EmailMCPServer) requireAuth(next http.Handler) http.Handler {
	want := []byte("Bearer " + s.config.AuthToken)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		got := []byte(r.Header.Get("Authorization"))

		// Compare in constant time so the response time does not reveal how
		// much of the token was correct. Lengths are checked first because
		// ConstantTimeCompare returns 0 for unequal lengths regardless.
		if len(got) != len(want) || subtle.ConstantTimeCompare(got, want) != 1 {
			log.Printf("   ⛔ Rejected request without a valid token from %s", r.RemoteAddr)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *EmailMCPServer) StartHTTP(ctx context.Context, addr string) error {
	log.Println("========================================")
	log.Println("🚀 Starting Email MCP Server (HTTP mode)...")
	log.Println("========================================")
	log.Printf("Server listening on %s", addr)
	log.Println("")

	// Without a token these endpoints let anyone who can reach the port read,
	// send and delete mail with the configured credentials. Refuse to start
	// rather than expose the mailbox.
	if s.config.AuthToken == "" {
		return fmt.Errorf("MCP_AUTH_TOKEN must be set to run in HTTP mode, otherwise the email tools on %s are open to any client that can reach them", addr)
	}

	mux := http.NewServeMux()

	// Health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok"}); err != nil {
			log.Printf("Error encoding health check response: %v", err)
		}
	})

	// MCP endpoints. These read, send and delete mail, so they sit behind the
	// bearer token. /health stays open so container health checks still work.
	mux.Handle("/mcp", s.requireAuth(http.HandlerFunc(s.handleMCPRequest)))
	mux.Handle("/sse", s.requireAuth(http.HandlerFunc(s.handleSSEConnection)))
	mux.Handle("/messages", s.requireAuth(http.HandlerFunc(s.handleMCPMessages)))

	// cors.Default allows every origin, which would let any web page a browser
	// visits drive these endpoints. Allow only the configured origins; with
	// none configured there is no cross-origin access at all.
	corsHandler := cors.New(cors.Options{
		AllowedOrigins:   s.config.AllowedOrigins,
		AllowedMethods:   []string{http.MethodGet, http.MethodPost, http.MethodOptions},
		AllowedHeaders:   []string{"Content-Type", "Authorization"},
		AllowCredentials: false,
	})

	if len(s.config.AllowedOrigins) == 0 {
		log.Println("   🔒 CORS: no cross-origin access (set MCP_ALLOWED_ORIGINS to allow specific origins)")
	} else {
		log.Printf("   🔒 CORS: allowed origins %v", s.config.AllowedOrigins)
	}

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           corsHandler.Handler(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Start server in a goroutine
	errChan := make(chan error, 1)
	go func() {
		log.Printf("✅ HTTP server started on http://%s", addr)
		log.Printf("   Health check: http://%s/health", addr)
		log.Printf("   MCP endpoint: http://%s/mcp", addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errChan <- err
		}
	}()

	// Wait for context cancellation or error
	select {
	case <-ctx.Done():
		log.Println("🛑 Shutting down HTTP server...")
		return httpServer.Shutdown(context.Background())
	case err := <-errChan:
		return err
	}
}

func (s *EmailMCPServer) handleMCPRequest(w http.ResponseWriter, r *http.Request) {
	log.Printf("🌐 Received MCP request: %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)

	// Only allow POST requests for MCP protocol
	if r.Method != http.MethodPost {
		log.Printf("   ❌ Method %s not allowed", r.Method)
		http.Error(w, fmt.Sprintf("Method %s not allowed, only POST is supported", r.Method), http.StatusMethodNotAllowed)
		return
	}

	log.Println("   📝 Processing POST request for MCP protocol")
	var request map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		log.Printf("   ❌ Invalid JSON request: %v", err)
		http.Error(w, fmt.Sprintf("Invalid request: %v", err), http.StatusBadRequest)
		return
	}

	response, status := s.dispatch(request)
	if response == nil {
		// A notification: acknowledged, nothing to send back.
		w.WriteHeader(status)
		return
	}

	log.Printf("   ✅ Responding to id=%v", response["id"])

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("   ❌ Error encoding MCP response: %v", err)
	}
}

// dispatch runs one JSON-RPC request and returns the response to send along
// with the HTTP status. A nil response means the request was a notification,
// which has no reply. Errors come back as JSON-RPC error objects so that both
// transports report them the same way.
func (s *EmailMCPServer) dispatch(request map[string]interface{}) (map[string]interface{}, int) {
	log.Printf("   📦 Request id=%v method=%v", request["id"], request["method"])

	method, ok := request["method"].(string)
	if !ok {
		return jsonRPCError(request["id"], jsonRPCInvalidRequest, "Missing method"), http.StatusBadRequest
	}

	log.Printf("   🔧 Method: %s", method)

	switch method {
	case "initialize":
		return jsonRPCResult(request["id"], map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{},
			},
			"serverInfo": map[string]interface{}{
				"name":    "email-mcp",
				"version": "0.1.0",
			},
		}), http.StatusOK

	case "notifications/initialized":
		log.Println("   ℹ️  Client initialized notification received")
		return nil, http.StatusAccepted

	case "tools/list":
		return jsonRPCResult(request["id"], s.listTools()), http.StatusOK

	case "tools/call":
		params, _ := request["params"].(map[string]interface{})
		toolName, _ := params["name"].(string)
		arguments, _ := params["arguments"].(map[string]interface{})

		result, err := s.callTool(toolName, arguments)
		if err != nil {
			log.Printf("   ❌ Error handling request: %v", err)
			return jsonRPCError(request["id"], jsonRPCInternalError, err.Error()), http.StatusOK
		}

		return jsonRPCResult(request["id"], result), http.StatusOK

	default:
		log.Printf("   ❌ Unknown method: %s", method)
		return jsonRPCError(request["id"], jsonRPCMethodNotFound, "Unknown method: "+method), http.StatusOK
	}
}

// JSON-RPC 2.0 error codes used by the transports.
const (
	jsonRPCInvalidRequest = -32600
	jsonRPCMethodNotFound = -32601
	jsonRPCInternalError  = -32603
)

func jsonRPCResult(id interface{}, result interface{}) map[string]interface{} {
	return map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	}
}

func jsonRPCError(id interface{}, code int, message string) map[string]interface{} {
	return map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
		},
	}
}

// sseSession is one open SSE stream. Responses for that client are queued on
// messages and written by the goroutine serving the stream.
type sseSession struct {
	messages chan []byte
	done     chan struct{}
}

// sseQueueSize bounds how many pending responses a slow client may accumulate
// before the server stops waiting for it.
const sseQueueSize = 16

// handleSSEConnection opens the event stream for an MCP SSE client.
//
// The transport is two-sided: the client opens this stream with GET, and the
// server answers with an "endpoint" event naming the URL to POST requests to.
// That URL carries a session id, which is how a reply arriving here is matched
// to the request that produced it. Replies travel on this stream as "message"
// events, never as the body of the POST.
func (s *EmailMCPServer) handleSSEConnection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Only GET is supported for the SSE stream", http.StatusMethodNotAllowed)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming is not supported by this connection", http.StatusInternalServerError)
		return
	}

	sessionID, err := newSessionID()
	if err != nil {
		log.Printf("   ❌ Failed to create a session id: %v", err)
		http.Error(w, "Failed to create a session", http.StatusInternalServerError)
		return
	}

	session := &sseSession{
		messages: make(chan []byte, sseQueueSize),
		done:     make(chan struct{}),
	}
	s.sseSessions.Store(sessionID, session)

	defer func() {
		s.sseSessions.Delete(sessionID)
		close(session.done)
		log.Printf("   🔌 SSE session %s closed", sessionID)
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Proxies that buffer would defeat the point of a stream.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	log.Printf("   🔗 SSE session %s opened for %s", sessionID, r.RemoteAddr)

	if _, err := fmt.Fprintf(w, "event: endpoint\ndata: /messages?sessionId=%s\n\n", sessionID); err != nil {
		log.Printf("   ❌ Failed to send the endpoint event: %v", err)
		return
	}
	flusher.Flush()

	// Comments keep idle connections from being closed by intermediaries.
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return

		case message := <-session.messages:
			if _, err := fmt.Fprintf(w, "event: message\ndata: %s\n\n", message); err != nil {
				log.Printf("   ❌ Failed to write an SSE message: %v", err)
				return
			}
			flusher.Flush()

		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// handleMCPMessages accepts a JSON-RPC request belonging to an open SSE
// session. The reply is queued on that session's stream, and the POST itself
// is answered with 202, as the SSE transport requires.
func (s *EmailMCPServer) handleMCPMessages(w http.ResponseWriter, r *http.Request) {
	log.Printf("📨 Received MCP message: %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)

	if r.Method != http.MethodPost {
		http.Error(w, fmt.Sprintf("Method %s not allowed, only POST is supported", r.Method), http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		http.Error(w, "Missing sessionId", http.StatusBadRequest)
		return
	}

	stored, ok := s.sseSessions.Load(sessionID)
	if !ok {
		// The stream is gone, so there is nowhere to deliver a reply.
		http.Error(w, "Unknown or closed session", http.StatusNotFound)
		return
	}
	session := stored.(*sseSession)

	var request map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		log.Printf("   ❌ Invalid JSON request: %v", err)
		http.Error(w, fmt.Sprintf("Invalid request: %v", err), http.StatusBadRequest)
		return
	}

	response, _ := s.dispatch(request)
	if response == nil {
		// A notification has no reply, but it was accepted.
		w.WriteHeader(http.StatusAccepted)
		return
	}

	encoded, err := json.Marshal(response)
	if err != nil {
		log.Printf("   ❌ Failed to encode the response: %v", err)
		http.Error(w, "Failed to encode the response", http.StatusInternalServerError)
		return
	}

	select {
	case session.messages <- encoded:
		log.Printf("   ✅ Queued response for id=%v on session %s", response["id"], sessionID)
		w.WriteHeader(http.StatusAccepted)

	case <-session.done:
		http.Error(w, "Session closed before the response could be delivered", http.StatusGone)

	case <-time.After(5 * time.Second):
		// The client is not reading its stream; do not block this handler.
		log.Printf("   ⚠️  Session %s is not draining its stream", sessionID)
		http.Error(w, "Session is not reading its event stream", http.StatusServiceUnavailable)
	}
}

// newSessionID returns an unguessable identifier for an SSE session.
func newSessionID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}

	return hex.EncodeToString(raw), nil
}

func (s *EmailMCPServer) listTools() interface{} {
	return map[string]interface{}{
		"tools": []interface{}{
			tools.ListMailboxesTool(),
			tools.SearchEmailsTool(),
			tools.GetEmailTool(),
			tools.SendEmailTool(),
			tools.ReplyToEmailTool(),
			tools.ForwardEmailTool(),
			tools.MarkAsReadTool(),
			tools.MarkAsUnreadTool(),
			tools.MoveEmailTool(),
			tools.DeleteEmailTool(),
		},
	}
}

func (s *EmailMCPServer) callTool(toolName string, arguments map[string]interface{}) (interface{}, error) {
	var result *mcp.CallToolResult
	var err error

	switch toolName {
	case "list_mailboxes":
		result, err = s.handleListMailboxes(arguments)
	case "search_emails":
		result, err = s.handleSearchEmails(arguments)
	case "get_email":
		result, err = s.handleGetEmail(arguments)
	case "send_email":
		result, err = s.handleSendEmail(arguments)
	case "reply_to_email":
		result, err = s.handleReplyToEmail(arguments)
	case "forward_email":
		result, err = s.handleForwardEmail(arguments)
	case "mark_as_read":
		result, err = s.handleMarkAsRead(arguments)
	case "mark_as_unread":
		result, err = s.handleMarkAsUnread(arguments)
	case "move_email":
		result, err = s.handleMoveEmail(arguments)
	case "delete_email":
		result, err = s.handleDeleteEmail(arguments)
	default:
		return nil, fmt.Errorf("unknown tool: %s", toolName)
	}

	if err != nil {
		return nil, err
	}

	return result, nil
}

func (s *EmailMCPServer) Close() error {
	log.Println("🛑 Shutting down Email MCP Server...")
	if s.imapClient != nil {
		if err := s.imapClient.Close(); err != nil {
			log.Printf("⚠️  Error closing IMAP client: %v", err)
			return err
		}
		log.Println("✅ IMAP client closed")
	}
	log.Println("👋 Email MCP Server stopped")
	return nil
}
