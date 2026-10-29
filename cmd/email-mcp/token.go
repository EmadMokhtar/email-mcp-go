package main

// This file holds the "token" commands: create, list and revoke. A token lets
// an MCP client call "serve --http". The store keeps only its hash, so "create"
// prints the token once, on standard output, and never again.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
	"github.com/EmadMokhtar/email-mcp-go/internal/tokens"
)

// scopeAll stands for every known scope in --scopes.
const scopeAll = "all"

// tokenCommands are the subcommands of "token".
var tokenCommands = []command{
	{name: "create", summary: "make a token: create --name NAME --scopes SCOPES (printed once)", run: runTokenCreate},
	{name: "list", summary: "list the tokens (never the token text)", run: runTokenList},
	{name: "revoke", summary: "revoke a token: revoke ID", run: runTokenRevoke},
}

func runToken(ctx context.Context, c *cli, args []string) int {
	return runGroup(ctx, c, "token", tokenCommands, args)
}

func runTokenCreate(ctx context.Context, c *cli, args []string) int {
	const cmd = "token create"
	fs := c.newFlags(cmd)
	dataDir := fs.String("data-dir", "", dataDirUsage)
	name := fs.String("name", "", "a name for the token, such as the client that uses it (required)")
	scopesFlag := fs.String("scopes", "", "what the token allows, as a comma-separated list of "+
		strings.Join(policy.KnownScopes(), ", ")+", or all (required)")
	rest, code, ok := c.parseFlags(cmd, fs, args)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		return c.unexpected(cmd)
	}
	if strings.TrimSpace(*name) == "" {
		return c.usage(cmd, "--name is required")
	}
	scopes, err := parseScopes(*scopesFlag)
	if err != nil {
		return c.usage(cmd, "%v", err)
	}

	a := c.openApp(ctx, cmd, *dataDir)
	if a == nil {
		return exitError
	}
	defer c.closeApp(ctx, cmd, a)
	token, err := a.Service.CreateToken(userContext(ctx), *name, scopes)
	switch {
	case errors.Is(err, tokens.ErrInvalidName):
		c.errorf("%s: the name must be 1 to 128 characters of text.\n", cmd)
		return exitError
	case err != nil:
		c.errorf("%s: %v\n", cmd, err)
		return exitError
	}
	c.errorf("Copy this token now. It is shown only once. It allows: %s.\n", strings.Join(scopes, ", "))
	c.printf("%s\n", token)
	return exitOK
}

// parseScopes reads --scopes: a comma-separated list of known scopes, or
// "all", which stands for every known scope. A token never gets no scope.
func parseScopes(text string) (policy.Scopes, error) {
	known := policy.KnownScopes()
	var scopes policy.Scopes
	for _, part := range strings.Split(text, ",") {
		part = strings.TrimSpace(part)
		switch {
		case part == "":
			continue
		case part == scopeAll:
			scopes = append(scopes, known...)
		case policy.KnownScope(part):
			scopes = append(scopes, part)
		default:
			// The text is not repeated: it could be anything the user typed.
			return nil, fmt.Errorf("--scopes has a scope that is not known: use %s, or all", strings.Join(known, ", "))
		}
	}
	if len(scopes) == 0 {
		return nil, fmt.Errorf("--scopes is required: a token allows nothing by default. Give a comma-separated "+
			"list of %s, or all", strings.Join(known, ", "))
	}
	slices.Sort(scopes)
	return slices.Compact(scopes), nil
}

func runTokenList(ctx context.Context, c *cli, args []string) int {
	const cmd = "token list"
	fs := c.newFlags(cmd)
	dataDir := fs.String("data-dir", "", dataDirUsage)
	rest, code, ok := c.parseFlags(cmd, fs, args)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		return c.unexpected(cmd)
	}
	a := c.openApp(ctx, cmd, *dataDir)
	if a == nil {
		return exitError
	}
	defer c.closeApp(ctx, cmd, a)
	list, err := a.Tokens.List(ctx, store.LocalTenantID)
	if err != nil {
		c.errorf("%s: %v\n", cmd, err)
		return exitError
	}
	if len(list) == 0 {
		c.printf("No access token was made yet. Make one with \"email-mcp token create\".\n")
		return exitOK
	}
	// The store keeps no time of last use, so the list has none.
	tw := tabwriter.NewWriter(c.stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tNAME\tSCOPES\tCREATED\tSTATUS")
	for _, t := range list {
		status := "valid"
		if !t.RevokedAt.IsZero() {
			status = "revoked " + formatTime(t.RevokedAt)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", t.ID, t.Name, strings.Join(t.Scopes, ","), formatTime(t.CreatedAt), status)
	}
	_ = tw.Flush()
	return exitOK
}

// formatTime writes a time in UTC, to the minute.
func formatTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

func runTokenRevoke(ctx context.Context, c *cli, args []string) int {
	const cmd = "token revoke"
	fs := c.newFlags(cmd)
	dataDir := fs.String("data-dir", "", dataDirUsage)
	rest, code, ok := c.parseFlags(cmd, fs, args)
	if !ok {
		return code
	}
	if len(rest) != 1 {
		return c.usage(cmd, "give one token id (\"email-mcp token list\" shows them)")
	}
	a := c.openApp(ctx, cmd, *dataDir)
	if a == nil {
		return exitError
	}
	defer c.closeApp(ctx, cmd, a)
	err := a.Service.RevokeToken(userContext(ctx), rest[0])
	switch {
	case errors.Is(err, tokens.ErrNotFound):
		c.errorf("%s: No token has this id. \"email-mcp token list\" shows the ids.\n", cmd)
		return exitError
	case err != nil:
		c.errorf("%s: %v\n", cmd, err)
		return exitError
	}
	c.printf("Revoked the token %s. A client that uses it is refused from now on.\n", rest[0])
	return exitOK
}
