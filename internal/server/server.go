package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/EmadMokhtar/email-mcp-go/internal/config"
	"github.com/EmadMokhtar/email-mcp-go/internal/imap"
	"github.com/EmadMokhtar/email-mcp-go/internal/smtp"
	"github.com/EmadMokhtar/email-mcp-go/internal/tools"
	"github.com/EmadMokhtar/email-mcp-go/pkg/models"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
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

	// Initialize IMAP client
	log.Printf("📧 Connecting to IMAP server: %s:%s (TLS: %v)", cfg.IMAPHost, cfg.IMAPPort, cfg.IMAPTLS)
	imapClient, err := imap.NewClient(cfg)
	if err != nil {
		log.Fatalf("❌ Failed to create IMAP client: %v", err)
	}
	s.imapClient = imapClient
	log.Println("✅ IMAP client initialized successfully")

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
