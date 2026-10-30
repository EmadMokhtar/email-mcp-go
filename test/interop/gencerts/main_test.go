package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestWriteCerts checks the files that the interop servers and the interop test
// read: a CA, and a server certificate with its key that chains to the CA for
// both names of the loopback address.
func TestWriteCerts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "certs")
	now := time.Now()
	require.NoError(t, writeCerts(dir, now))

	caPEM, err := os.ReadFile(filepath.Join(dir, caFile)) //nolint:gosec // G304: a file inside t.TempDir()
	require.NoError(t, err)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(caPEM), "ca.crt holds a PEM certificate")

	pair, err := tls.LoadX509KeyPair(filepath.Join(dir, certFile), filepath.Join(dir, keyFile))
	require.NoError(t, err, "the key fits the server certificate")
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	require.NoError(t, err)

	for _, name := range []string{"localhost", "127.0.0.1"} {
		_, err := leaf.Verify(x509.VerifyOptions{
			DNSName:     name,
			Roots:       roots,
			CurrentTime: now,
			KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		})
		require.NoError(t, err, "the server certificate is valid for %s", name)
	}
	_, err = leaf.Verify(x509.VerifyOptions{DNSName: "example.com", Roots: roots, CurrentTime: now})
	require.Error(t, err, "the server certificate is not valid for another name")
	require.False(t, leaf.IsCA, "the server certificate cannot sign other certificates")

	// The CA key is not written: nobody may sign more certificates with it.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	require.ElementsMatch(t, []string{caFile, certFile, keyFile}, names)
	block, _ := pem.Decode(caPEM)
	require.NotNil(t, block)
	require.Equal(t, "CERTIFICATE", block.Type)
}

// TestWriteCertsReplaces checks that a second run makes a new CA and replaces
// the old files, so a stale certificate never stays behind.
func TestWriteCertsReplaces(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, writeCerts(dir, time.Now()))
	first, err := os.ReadFile(filepath.Join(dir, caFile)) //nolint:gosec // G304: a file inside t.TempDir()
	require.NoError(t, err)
	require.NoError(t, writeCerts(dir, time.Now()))
	second, err := os.ReadFile(filepath.Join(dir, caFile)) //nolint:gosec // G304: a file inside t.TempDir()
	require.NoError(t, err)
	require.NotEqual(t, first, second)
}
