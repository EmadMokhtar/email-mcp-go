// Package webtest helps tests reach the local web interface by its own name.
//
// The links of the web interface use the name "email-mcp.localhost". A browser
// knows that a name under "localhost" is the computer itself, but the name
// lookup of a test may not know it, for example on a CI runner. So a test
// connects to 127.0.0.1 itself and keeps the name in the URL and the Host
// header, as a browser does.
package webtest

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
)

// Transport returns a transport that connects to 127.0.0.1 on the port of the
// URL, whatever the name in the URL is. It uses no proxy.
func Transport() *http.Transport {
	var d net.Dialer
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			return d.DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
		},
	}
}

// Addr returns the address to connect to for a base URL of the web interface:
// "127.0.0.1:<port>".
func Addr(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	if u.Port() == "" {
		return "", fmt.Errorf("webtest: the URL has no port")
	}
	return net.JoinHostPort("127.0.0.1", u.Port()), nil
}
