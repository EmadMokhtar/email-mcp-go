// Command gencerts makes the TLS files of the interop test servers:
//
//	go run ./test/interop/gencerts test/interop/certs
//
// It writes a new test CA (ca.crt) and a server certificate signed by it
// (server.crt, server.key) that is valid for "localhost" and "127.0.0.1". The
// CA key stays in memory and is thrown away, so nothing else can be signed
// with it. The files are made for each run and are never committed.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// The names of the files in the output directory.
const (
	caFile   = "ca.crt"
	certFile = "server.crt"
	keyFile  = "server.key"
)

// validity is how long the certificates are valid. A run makes new ones, so
// a short time is enough.
const validity = 7 * 24 * time.Hour

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: gencerts <output directory>")
		os.Exit(2)
	}
	if err := writeCerts(os.Args[1], time.Now()); err != nil {
		fmt.Fprintln(os.Stderr, "gencerts:", err)
		os.Exit(1)
	}
}

// writeCerts makes a new CA and a server certificate and writes them to dir.
// It replaces files from an earlier run.
func writeCerts(dir string, now time.Time) error {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "email-mcp interop test CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(validity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return err
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return err
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	template := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(validity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: the directory holds test files that the containers must read
		return err
	}
	if err := writePEM(filepath.Join(dir, caFile), "CERTIFICATE", caDER); err != nil {
		return err
	}
	if err := writePEM(filepath.Join(dir, certFile), "CERTIFICATE", der); err != nil {
		return err
	}
	return writePEM(filepath.Join(dir, keyFile), "PRIVATE KEY", keyDER)
}

// writePEM writes one PEM block to a file.
//
// Every file is readable by all users, the key too. The servers in the
// containers run as other users than the one who made the files (Dovecot runs
// as "vmail"), and they must read the key. It is a test key for the loopback
// address only, and it is made new for every run.
func writePEM(path, blockType string, der []byte) error {
	data := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
	if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // G306: see the comment of writePEM
		return err
	}
	// WriteFile keeps the mode of a file that is there already.
	return os.Chmod(path, 0o644) //nolint:gosec // G302: see the comment of writePEM
}

// serial returns a random serial number of 128 bits.
func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		// crypto/rand does not fail on the supported platforms.
		panic(err)
	}
	return n
}
