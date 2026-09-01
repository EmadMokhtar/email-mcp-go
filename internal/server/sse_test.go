package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EmadMokhtar/email-mcp-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readSSEEvent reads one "event:/data:" pair, skipping keep-alive comments.
func readSSEEvent(t *testing.T, reader *bufio.Reader) (event, data string) {
	t.Helper()

	for {
		line, err := reader.ReadString('\n')
		require.NoError(t, err)
		line = strings.TrimRight(line, "\r\n")

		switch {
		case strings.HasPrefix(line, ":"), line == "":
			continue
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			return event, strings.TrimPrefix(line, "data: ")
		}
	}
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	s := &EmailMCPServer{config: &config.Config{}}

	mux := http.NewServeMux()
	mux.HandleFunc("/sse", s.handleSSEConnection)
	mux.HandleFunc("/messages", s.handleMCPMessages)

	return httptest.NewServer(mux)
}

func TestSSEDeliversResponsesOnTheStream(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/sse", nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	// The stream must first name the endpoint to post to, including a session
	// id. Without it a client has nowhere to send requests.
	reader := bufio.NewReader(resp.Body)
	event, data := readSSEEvent(t, reader)
	assert.Equal(t, "endpoint", event)
	require.True(t, strings.HasPrefix(data, "/messages?sessionId="), "got %q", data)
	assert.NotEmpty(t, strings.TrimPrefix(data, "/messages?sessionId="))

	// Post a request to that endpoint. The POST is only acknowledged.
	body := bytes.NewBufferString(`{"jsonrpc":"2.0","id":7,"method":"tools/list"}`)
	postResp, err := http.Post(srv.URL+data, "application/json", body)
	require.NoError(t, err)
	defer postResp.Body.Close()

	assert.Equal(t, http.StatusAccepted, postResp.StatusCode,
		"the SSE transport acknowledges the POST and replies on the stream")

	// The actual reply arrives as a message event on the stream.
	event, data = readSSEEvent(t, reader)
	assert.Equal(t, "message", event)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(data), &parsed))
	assert.Equal(t, "2.0", parsed["jsonrpc"])
	assert.Equal(t, float64(7), parsed["id"])
	assert.NotNil(t, parsed["result"], "tools/list must return a result")
}

func TestSSEMessagesRejectsUnknownSession(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	tests := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{"no session id", "/messages", http.StatusBadRequest},
		{"unknown session id", "/messages?sessionId=deadbeef", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
			resp, err := http.Post(srv.URL+tt.path, "application/json", body)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, tt.wantStatus, resp.StatusCode)
		})
	}
}

func TestSSESessionIsRemovedWhenTheClientDisconnects(t *testing.T) {
	s := &EmailMCPServer{config: &config.Config{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/sse", s.handleSSEConnection)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/sse", nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	_, data := readSSEEvent(t, bufio.NewReader(resp.Body))
	sessionID := strings.TrimPrefix(data, "/messages?sessionId=")

	_, present := s.sseSessions.Load(sessionID)
	assert.True(t, present, "session should be registered while the stream is open")

	cancel()
	resp.Body.Close()

	// The handler cleans up when the request context ends.
	assert.Eventually(t, func() bool {
		_, still := s.sseSessions.Load(sessionID)
		return !still
	}, 2*time.Second, 10*time.Millisecond, "session should be removed after disconnect")
}

func TestDispatchReportsUnknownMethodAsJSONRPCError(t *testing.T) {
	s := &EmailMCPServer{config: &config.Config{}}

	response, status := s.dispatch(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "does/not/exist",
	})

	require.NotNil(t, response)
	assert.Equal(t, http.StatusOK, status)

	rpcErr, ok := response["error"].(map[string]interface{})
	require.True(t, ok, "an unknown method must come back as a JSON-RPC error")
	assert.Equal(t, jsonRPCMethodNotFound, rpcErr["code"])
	assert.Equal(t, 3, response["id"])
}

func TestDispatchTreatsInitializedAsNotification(t *testing.T) {
	s := &EmailMCPServer{config: &config.Config{}}

	response, status := s.dispatch(map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})

	assert.Nil(t, response, "a notification has no reply")
	assert.Equal(t, http.StatusAccepted, status)
}

func TestDispatchInitializeAdvertisesTools(t *testing.T) {
	s := &EmailMCPServer{config: &config.Config{}}

	response, status := s.dispatch(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
	})

	require.NotNil(t, response)
	assert.Equal(t, http.StatusOK, status)

	result, ok := response["result"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "2024-11-05", result["protocolVersion"])

	_ = fmt.Sprint(result)
}
