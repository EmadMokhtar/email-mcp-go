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
	"io/fs"
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

// ErrNoKey is returned by LoadKeychainKEK when the keychain has no master key.
var ErrNoKey = errors.New("secrets: no master key is stored")

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
	return keychainKEK(encoded)
}

// LoadKeychainKEK is NewKeychainKEK without the creation: when the keychain has
// no key, it returns an error that matches ErrNoKey, and the keychain stays as
// it was. A check that must change nothing uses it.
func LoadKeychainKEK() (KEK, error) {
	encoded, err := keyring.Get(keychainService, keychainUser)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, fmt.Errorf("secrets: keychain entry %q/%q: %w", keychainService, keychainUser, ErrNoKey)
	}
	if err != nil {
		return nil, fmt.Errorf("secrets: keychain: %w", err)
	}
	return keychainKEK(encoded)
}

// keychainKEK returns the KEK of the text that the keychain holds.
func keychainKEK(encoded string) (KEK, error) {
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
// mode 0600, in the format that NewFileKEK reads. The file is on the disk when
// it returns, so a crash or a power loss right after cannot take it away: a
// database that depends on a key that is lost can never be read again. The first
// start of a data directory and a change of master key both use it.
//
// In this order it:
//  1. refuses, with an error that matches fs.ErrExist, when anything exists at
//     path (a file or a symbolic link). It never replaces a key;
//  2. writes the key to a new temporary file in the folder of path (mode
//     0600), calls Sync on it, and closes it;
//  3. renames the temporary file to path, and calls Sync on the folder, so the
//     new name is on the disk too (not on Windows, see syncDir).
//
// Steps 1 and 3 are not one step. The caller must hold a lock, so that no other
// program makes a file at path in between. On an error, the temporary file is
// removed and nothing is at path.
func GenerateKeyFile(path string) (err error) {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("secrets: create key file: %w", fs.ErrExist)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("secrets: create key file: %w", err)
	}
	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return fmt.Errorf("secrets: create key: %w", err)
	}
	dir := filepath.Dir(path)
	// CreateTemp makes the file with mode 0600, and never opens one that exists.
	f, err := os.CreateTemp(dir, ".email-mcp-key-*")
	if err != nil {
		return fmt.Errorf("secrets: create key file: %w", err)
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if _, err := io.WriteString(f, encodeKey(key)+"\n"); err != nil {
		return fmt.Errorf("secrets: write key file: %w", err)
	}
	if err := syncFile(f); err != nil {
		return fmt.Errorf("secrets: write key file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("secrets: write key file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("secrets: put the key file in place: %w", err)
	}
	if err := syncFolder(dir); err != nil {
		return fmt.Errorf("secrets: write key file: %w", err)
	}
	return nil
}

// The sync calls of the key file writer. Tests replace them to see the order.
var (
	syncFile   = (*os.File).Sync
	syncFolder = syncDir
)

// syncDir flushes the entries of the folder dir to the disk, so a new name in it
// survives a power loss. Windows cannot open a folder to sync it, and NTFS keeps
// the change of a name in its own journal, so this does nothing there.
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir) //nolint:gosec // G304: the folder of a key file that the user chose
	if err != nil {
		return err
	}
	err = d.Sync()
	if closeErr := d.Close(); err == nil {
		err = closeErr
	}
	return err
}
