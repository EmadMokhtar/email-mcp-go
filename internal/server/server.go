package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"

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
	mcpServer  *server.MCPServer
	imapClient *imap.Client
	smtpClient *smtp.Client
	config     *config.Config
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

func (s *EmailMCPServer) handleListMailboxes(arguments map[string]interface{}) (*mcp.CallToolResult, error) {
	log.Println("🔧 Tool called: list_mailboxes")
	log.Printf("   Arguments: %v", arguments)

	if s.imapClient == nil {
		return mcp.NewToolResultError("IMAP client not initialized - server running in testing mode"), nil
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
	log.Printf("   Arguments: %v", arguments)

	if s.imapClient == nil {
		log.Println("❌ IMAP client not initialized")
		return mcp.NewToolResultError("IMAP client not initialized - server running in testing mode"), nil
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

	log.Printf("   Search criteria: %+v", criteria)
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
	log.Printf("   Arguments: %v", arguments)

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

	log.Printf("✅ Retrieved email: %s", email.Subject)
	result, err := json.Marshal(email)
	if err != nil {
		log.Printf("❌ Failed to marshal result: %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Failed to marshal result: %v", err)), nil
	}

	return mcp.NewToolResultText(string(result)), nil
}

func (s *EmailMCPServer) handleSendEmail(arguments map[string]interface{}) (*mcp.CallToolResult, error) {
	log.Println("🔧 Tool called: send_email")
	log.Printf("   Arguments: %v", arguments)

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

	log.Printf("   Sending email to %v, subject: '%s'", emailReq.To, emailReq.Subject)
	if err := s.smtpClient.SendEmail(&emailReq); err != nil {
		log.Printf("❌ Failed to send email: %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("Failed to send email: %v", err)), nil
	}

	log.Println("✅ Email sent successfully")
	return mcp.NewToolResultText("Email sent successfully"), nil
}

func (s *EmailMCPServer) handleReplyToEmail(arguments map[string]interface{}) (*mcp.CallToolResult, error) {
	log.Println("🔧 Tool called: reply_to_email")
	log.Printf("   Arguments: %v", arguments)

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
	log.Printf("   Arguments: %v", arguments)

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

	log.Printf("   Forwarding email ID %d from folder '%s' to %v", params.EmailID, params.Folder, params.To)
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
	log.Printf("   Arguments: %v", arguments)

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
	log.Printf("   Arguments: %v", arguments)

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
	log.Printf("   Arguments: %v", arguments)

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
	log.Printf("   Arguments: %v", arguments)

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
func (s *EmailMCPServer) StartHTTP(ctx context.Context, addr string) error {
	log.Println("========================================")
	log.Println("🚀 Starting Email MCP Server (HTTP mode)...")
	log.Println("========================================")
	log.Printf("Server listening on %s", addr)
	log.Println("")

	mux := http.NewServeMux()

	// Health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok"}); err != nil {
			log.Printf("Error encoding health check response: %v", err)
		}
	})

	// MCP endpoints
	mux.HandleFunc("/mcp", s.handleMCPRequest)
	mux.HandleFunc("/sse", s.handleSSEConnection)
	mux.HandleFunc("/messages", s.handleMCPMessages)

	handler := cors.Default().Handler(mux)

	// Create HTTP server and apply CORS middleware to the entire mux
	httpServer := &http.Server{
		Addr:    addr,
		Handler: handler,
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

	// Handle CORS preflight requests
	if r.Method == http.MethodOptions {
		log.Println("   ✅ Handling CORS preflight request")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(http.StatusOK)
		return
	}

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

	log.Printf("   📦 Request body: %+v", request)

	// Extract method
	method, ok := request["method"].(string)
	if !ok {
		log.Println("   ❌ Missing 'method' field in request")
		http.Error(w, "Missing method", http.StatusBadRequest)
		return
	}

	log.Printf("   🔧 Method: %s", method)

	var result interface{}
	var err error

	switch method {
	case "initialize":
		result = map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{},
			},
			"serverInfo": map[string]interface{}{
				"name":    "email-mcp",
				"version": "0.1.0",
			},
		}
	case "notifications/initialized":
		// This is a notification, not a request - no response needed
		log.Println("   ℹ️  Client initialized notification received")
		w.WriteHeader(http.StatusOK)
		return
	case "tools/list":
		result = s.listTools()
	case "tools/call":
		params, _ := request["params"].(map[string]interface{})
		toolName, _ := params["name"].(string)
		arguments, _ := params["arguments"].(map[string]interface{})
		result, err = s.callTool(toolName, arguments)
	default:
		log.Printf("   ❌ Unknown method: %s", method)
		http.Error(w, fmt.Sprintf("Unknown method: %s", method), http.StatusBadRequest)
		return
	}

	if err != nil {
		log.Printf("   ❌ Error handling request: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      request["id"],
		"result":  result,
	}

	log.Printf("   ✅ Sending response: %+v", response)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("   ❌ Error encoding MCP response: %v", err)
	}
}

func (s *EmailMCPServer) handleMCPMessages(w http.ResponseWriter, r *http.Request) {
	log.Printf("📨 Received MCP messages request: %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)

	// Handle CORS preflight requests
	if r.Method == http.MethodOptions {
		log.Println("   ✅ Handling CORS preflight request for messages")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(http.StatusOK)
		return
	}

	// Only allow POST requests for MCP protocol
	if r.Method != http.MethodPost {
		log.Printf("   ❌ Method %s not allowed for messages endpoint", r.Method)
		http.Error(w, fmt.Sprintf("Method %s not allowed, only POST is supported", r.Method), http.StatusMethodNotAllowed)
		return
	}

	// Delegate to the main MCP handler
	log.Println("   🔄 Delegating to main MCP handler")
	s.handleMCPRequest(w, r)
}

func (s *EmailMCPServer) handleSSEConnection(w http.ResponseWriter, r *http.Request) {
	// Set headers for SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Send initial connection message
	if _, err := fmt.Fprintf(w, "event: endpoint\ndata: /mcp\n\n"); err != nil {
		log.Printf("Error writing SSE message: %v", err)
		return
	}

	// Flush the response writer
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	// Keep connection alive
	<-r.Context().Done()
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
