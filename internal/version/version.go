// Package version holds the build version of email-mcp.
package version

// Version is the release version of the binary. It is "dev" for a plain
// "go build". A release build sets it with the linker, for example:
//
//	go build -ldflags "-X github.com/EmadMokhtar/email-mcp-go/internal/version.Version=v1.2.3"
var Version = "dev"
