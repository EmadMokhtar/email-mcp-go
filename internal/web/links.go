package web

// This file holds the links that the service gives to the agent. A link never
// carries a token or a session: it works only in a browser that the user
// signed in with "email-mcp setup".

import (
	"context"
	"errors"
	"net/url"
	"strconv"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
)

// Links builds the addresses of the pages. It implements service.Links. Get one
// with Server.Links.
//
// Each method starts the server first (EnsureStarted), so a stdio process
// starts the web interface the first time a tool needs a link. Ids are escaped
// as one path segment, so an id cannot add a path, a query or a fragment.
type Links struct {
	s *Server
}

// Links is a service.Links.
var _ service.Links = Links{}

// errEmptyID means that a link was asked for an empty id.
var errEmptyID = errors.New("web: a link needs an id")

// Links returns the link builder of the server.
func (s *Server) Links() Links {
	return Links{s: s}
}

// Connect returns the address of the page where the user connects an account:
// "http://127.0.0.1:<port>/connect".
func (l Links) Connect(ctx context.Context) (string, error) {
	base, err := l.s.EnsureStarted(ctx)
	if err != nil {
		return "", err
	}
	return base + "/connect", nil
}

// Settings returns the address of the settings page of an account, with the
// area and the level to show first:
// "http://127.0.0.1:<port>/accounts/<id>/settings?area=mail&level=4". The page
// changes nothing until the user submits it.
func (l Links) Settings(ctx context.Context, id account.AccountID, area account.Area, level account.Level) (string, error) {
	if id == "" {
		return "", errEmptyID
	}
	base, err := l.s.EnsureStarted(ctx)
	if err != nil {
		return "", err
	}
	query := url.Values{"area": {string(area)}, "level": {strconv.Itoa(int(level))}}
	return base + "/accounts/" + url.PathEscape(string(id)) + "/settings?" + query.Encode(), nil
}

// Approval returns the address of the page where the user approves or rejects
// a pending action: "http://127.0.0.1:<port>/approvals/<id>".
func (l Links) Approval(ctx context.Context, pendingID string) (string, error) {
	if pendingID == "" {
		return "", errEmptyID
	}
	base, err := l.s.EnsureStarted(ctx)
	if err != nil {
		return "", err
	}
	return base + "/approvals/" + url.PathEscape(pendingID), nil
}
