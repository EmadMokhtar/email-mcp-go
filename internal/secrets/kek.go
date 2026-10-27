package secrets

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/zalando/go-keyring"
)

const (
	// kekAAD is the fixed associated data for wrapping a data key with a local
	// master key.
	kekAAD = "email-mcp/v1|kek"

	// Names of the master key entry in the operating system keychain.
	keychainService = "email-mcp"
	keychainUser    = "master-key"

	// keychainKEKID is the ID of the keychain master key. The "v1" is the
	// version of the key layout, not a key rotation counter.
	keychainKEKID = "keychain:v1"

	// maxKeyFileSize limits how much of a key file is read. A valid key file is
	// 44 characters plus some whitespace.
	maxKeyFileSize = 1024
)

// ErrKeyFilePermissions is returned by NewFileKEK when other users can read or
// change the key file.
var ErrKeyFilePermissions = errors.New("secrets: key file permissions are too open")

// errInvalidKey means that a stored master key is not standard base64 of 32 bytes.
var errInvalidKey = errors.New("master key must be standard base64 of exactly 32 bytes")

// KEK is a key-encryption key (also called a master key). It protects the data
// keys that are stored in the database: the database holds only wrapped keys.
//
// A KEK can be local (the implementations in this file) or remote, for example
// a cloud key management service. That is why the methods take a context.
type KEK interface {
	// ID names this master key. It is stored next to every wrapped key, so the
	// right KEK can be found later.
	ID() string
	// Wrap encrypts a data key.
	Wrap(ctx context.Context, dek []byte) ([]byte, error)
	// Unwrap decrypts a value made by Wrap. It returns ErrDecrypt on failure.
	Unwrap(ctx context.Context, wrapped []byte) ([]byte, error)
}

// localKEK is a KEK whose 32-byte key is held in memory. It wraps with Seal
// and the fixed associated data kekAAD.
type localKEK struct {
	id  string
	key []byte
}

func (k *localKEK) ID() string { return k.id }

// Wrap and Unwrap do no input or output, so they do not use the context.

func (k *localKEK) Wrap(_ context.Context, dek []byte) ([]byte, error) {
	return Seal(k.key, dek, kekAAD)
}

func (k *localKEK) Unwrap(_ context.Context, wrapped []byte) ([]byte, error) {
	return Open(k.key, wrapped, kekAAD)
}

// encodeKey returns the text form of a master key: standard base64.
func encodeKey(key []byte) string {
	return base64.StdEncoding.EncodeToString(key)
}

// decodeKey is the reverse of encodeKey. It is strict: it accepts only
// standard base64 with padding that decodes to exactly 32 bytes. The error
// never contains the text it was given.
func decodeKey(encoded string) ([]byte, error) {
	// The length check also rejects line breaks inside the text. The base64
	// decoder would skip them without an error.
	if len(encoded) != base64.StdEncoding.EncodedLen(keySize) {
		return nil, errInvalidKey
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != keySize {
		return nil, errInvalidKey
	}
	return key, nil
}

// NewKeychainKEK returns a KEK whose key is stored in the operating system
// keychain (service "email-mcp", user "master-key"). It creates a new random
// key when the keychain has none.
//
// If the keychain holds a value that is not a valid key, it returns an error.
// It never replaces an existing value, because that would make all data
// wrapped with the old key unreadable.
func NewKeychainKEK() (KEK, error) {
	encoded, err := keyring.Get(keychainService, keychainUser)
	if errors.Is(err, keyring.ErrNotFound) {
		encoded, err = createKeychainKey()
	}
	if err != nil {
		return nil, fmt.Errorf("secrets: keychain: %w", err)
	}
	key, err := decodeKey(encoded)
	if err != nil {
		return nil, fmt.Errorf("secrets: keychain entry %q/%q: %w", keychainService, keychainUser, err)
	}
	return &localKEK{id: keychainKEKID, key: key}, nil
}

// createKeychainKey stores a new random key in the keychain and returns the
// stored text. It reads the entry back, so the caller uses the value that is
// really stored. The keychain has no "create only if missing" call, so this
// also covers most of the case where another process creates the key at the
// same time.
func createKeychainKey() (string, error) {
	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return "", fmt.Errorf("create key: %w", err)
	}
	if err := keyring.Set(keychainService, keychainUser, encodeKey(key)); err != nil {
		return "", fmt.Errorf("store new key: %w", err)
	}
	stored, err := keyring.Get(keychainService, keychainUser)
	if err != nil {
		return "", fmt.Errorf("read back new key: %w", err)
	}
	return stored, nil
}

// NewFileKEK returns a KEK whose key is read from a file. The file holds
// standard base64 of exactly 32 bytes. Whitespace before and after the key is
// allowed. Use GenerateKeyFile to create one.
//
// On systems with Unix file modes it returns ErrKeyFilePermissions when the
// group or other users have any permission on the file (a mode broader than
// 0600). Windows has no such modes, so the check is skipped there.
func NewFileKEK(path string) (KEK, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("secrets: key file path: %w", err)
	}
	f, err := os.Open(absPath) //nolint:gosec // G304: the user chooses the key file path on purpose
	if err != nil {
		return nil, fmt.Errorf("secrets: open key file: %w", err)
	}
	// The file is only read, so a close error has no effect on the result.
	defer func() { _ = f.Close() }()

	// Check the open file, not the path, so the file cannot be swapped between
	// the check and the read.
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("secrets: stat key file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("secrets: key file %s is not a regular file", absPath)
	}
	if mode := info.Mode().Perm(); runtime.GOOS != "windows" && mode&0o077 != 0 {
		return nil, fmt.Errorf("%w: %s has mode %04o, it must not be accessible to group or other users (use 0600)",
			ErrKeyFilePermissions, absPath, mode)
	}

	content, err := io.ReadAll(io.LimitReader(f, maxKeyFileSize))
	if err != nil {
		return nil, fmt.Errorf("secrets: read key file: %w", err)
	}
	key, err := decodeKey(strings.TrimSpace(string(content)))
	if err != nil {
		return nil, fmt.Errorf("secrets: key file %s: %w", absPath, err)
	}

	sum := sha256.Sum256([]byte(absPath))
	return &localKEK{id: "file:" + hex.EncodeToString(sum[:])[:8], key: key}, nil
}

// GenerateKeyFile creates a new random master key and writes it to path with
// mode 0600, in the format that NewFileKEK reads. It returns an error that
// matches fs.ErrExist when anything already exists at path (a file or a
// symbolic link), and never changes it.
func GenerateKeyFile(path string) (err error) {
	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return fmt.Errorf("secrets: create key: %w", err)
	}

	// O_EXCL makes the call fail if the path exists, and it does not follow a
	// symbolic link at the path.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: the user chooses the key file path on purpose
	if err != nil {
		return fmt.Errorf("secrets: create key file: %w", err)
	}
	// If writing fails, remove the new file. A half-written key file would
	// block the next attempt because the file already exists.
	defer func() {
		if err != nil {
			_ = os.Remove(path)
		}
	}()

	if _, err := io.WriteString(f, encodeKey(key)+"\n"); err != nil {
		_ = f.Close()
		return fmt.Errorf("secrets: write key file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("secrets: write key file: %w", err)
	}
	return nil
}
