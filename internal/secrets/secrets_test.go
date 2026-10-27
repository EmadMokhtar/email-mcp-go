package secrets

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

// randomBytes returns n random bytes.
func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return b
}

// writeKeyFile writes content to a new file with exactly the given mode.
// It calls Chmod so that the umask of the test process does not change the mode.
func writeKeyFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "master.key")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	require.NoError(t, os.Chmod(path, mode))
	return path
}

// readFile returns the content of a file in a test directory.
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path) //nolint:gosec // G304: tests only read files inside t.TempDir()
	require.NoError(t, err)
	return content
}

// skipOnWindows skips tests that depend on Unix file modes or symlinks.
func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix file modes are not available on Windows")
	}
}

// --- AAD ---

func TestAADFormat(t *testing.T) {
	require.Equal(t, "email-mcp/v1|tenant:t1|account:a1|field:password", AAD("t1", "a1", "password"))
}

// --- Seal and Open ---

func TestSealOpenRoundTrip(t *testing.T) {
	key := randomBytes(t, 32)
	aad := AAD("t1", "a1", "password")

	sealed, err := Seal(key, []byte("hunter2"), aad)
	require.NoError(t, err)

	plaintext, err := Open(key, sealed, aad)
	require.NoError(t, err)
	require.Equal(t, []byte("hunter2"), plaintext)
}

func TestSealOpenEmptyPlaintext(t *testing.T) {
	key := randomBytes(t, 32)

	sealed, err := Seal(key, nil, "aad")
	require.NoError(t, err)

	plaintext, err := Open(key, sealed, "aad")
	require.NoError(t, err)
	require.Empty(t, plaintext)
}

func TestSealOutputIsNonceThenCiphertext(t *testing.T) {
	key := randomBytes(t, 32)
	plaintext := []byte("some plaintext")

	sealed, err := Seal(key, plaintext, "aad")
	require.NoError(t, err)

	// 12 bytes of nonce, the ciphertext (same length as the plaintext) and a 16-byte tag.
	require.Len(t, sealed, 12+len(plaintext)+16)
	require.NotContains(t, string(sealed), string(plaintext))
}

func TestSealUsesFreshNonce(t *testing.T) {
	key := randomBytes(t, 32)

	first, err := Seal(key, []byte("same input"), "aad")
	require.NoError(t, err)
	second, err := Seal(key, []byte("same input"), "aad")
	require.NoError(t, err)

	require.NotEqual(t, first, second)
	require.NotEqual(t, first[:12], second[:12], "the nonce must be different for every call")
}

func TestOpenWrongAADFails(t *testing.T) {
	key := randomBytes(t, 32)

	sealed, err := Seal(key, []byte("hunter2"), AAD("t1", "a1", "password"))
	require.NoError(t, err)

	plaintext, err := Open(key, sealed, AAD("t1", "a2", "password"))
	require.ErrorIs(t, err, ErrDecrypt)
	require.Nil(t, plaintext)
}

func TestOpenWrongKeyFails(t *testing.T) {
	sealed, err := Seal(randomBytes(t, 32), []byte("hunter2"), "aad")
	require.NoError(t, err)

	plaintext, err := Open(randomBytes(t, 32), sealed, "aad")
	require.ErrorIs(t, err, ErrDecrypt)
	require.Nil(t, plaintext)
}

func TestOpenTamperedFails(t *testing.T) {
	key := randomBytes(t, 32)

	sealed, err := Seal(key, []byte("hunter2"), "aad")
	require.NoError(t, err)

	// Flip one bit in every position in turn: nonce, ciphertext and tag.
	for i := range sealed {
		tampered := bytes.Clone(sealed)
		tampered[i] ^= 0x01

		plaintext, err := Open(key, tampered, "aad")
		require.ErrorIs(t, err, ErrDecrypt, "byte %d", i)
		require.Nil(t, plaintext, "byte %d", i)
	}
}

func TestOpenTooShortFails(t *testing.T) {
	key := randomBytes(t, 32)

	sealed, err := Seal(key, nil, "aad")
	require.NoError(t, err)
	require.Len(t, sealed, 12+16, "an empty plaintext gives the shortest valid value")

	for n := 0; n < len(sealed); n++ {
		_, err := Open(key, sealed[:n], "aad")
		require.ErrorIs(t, err, ErrDecrypt, "length %d", n)
	}
	_, err = Open(key, nil, "aad")
	require.ErrorIs(t, err, ErrDecrypt)
}

// Every Open failure must return the same sentinel value. The caller must not
// learn why the decryption failed.
func TestOpenFailuresAreIndistinguishable(t *testing.T) {
	key := randomBytes(t, 32)
	sealed, err := Seal(key, []byte("hunter2"), "aad")
	require.NoError(t, err)

	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 0x01

	_, wrongKey := Open(randomBytes(t, 32), sealed, "aad")
	_, wrongAAD := Open(key, sealed, "other")
	_, modified := Open(key, tampered, "aad")
	_, tooShort := Open(key, sealed[:5], "aad")

	for name, err := range map[string]error{
		"wrong key": wrongKey, "wrong aad": wrongAAD, "tampered": modified, "too short": tooShort,
	} {
		require.Same(t, ErrDecrypt, err, name)
	}
}

func TestSealAndOpenRejectBadKeyLength(t *testing.T) {
	for _, size := range []int{0, 1, 16, 24, 31, 33, 64} {
		key := randomBytes(t, size)

		sealed, err := Seal(key, []byte("hunter2"), "aad")
		require.ErrorIs(t, err, ErrKeySize, "Seal with a %d-byte key", size)
		require.Nil(t, sealed)

		// Open also reports ErrDecrypt, as it does for every failure.
		valid, err := Seal(randomBytes(t, 32), []byte("hunter2"), "aad")
		require.NoError(t, err)
		plaintext, err := Open(key, valid, "aad")
		require.ErrorIs(t, err, ErrKeySize, "Open with a %d-byte key", size)
		require.ErrorIs(t, err, ErrDecrypt, "Open with a %d-byte key", size)
		require.Nil(t, plaintext)
	}
}

// --- Secret ---

type holder struct {
	Password Secret
}

func TestSecretNeverPrints(t *testing.T) {
	secret := Secret("hunter2")

	t.Run("methods", func(t *testing.T) {
		require.Equal(t, "[redacted]", secret.String())
		require.Equal(t, slog.StringValue("[redacted]"), secret.LogValue())
		require.Equal(t, "hunter2", secret.Reveal())
	})

	t.Run("fmt verbs", func(t *testing.T) {
		// %d, %x and %q are included because fmt handles them in different ways.
		for _, verb := range []string{"%v", "%s", "%+v", "%#v", "%q", "%x", "%d", "%10s", "%-12v"} {
			for name, value := range map[string]any{
				"value":   secret,
				"pointer": &secret,
				"field":   holder{Password: secret},
				"slice":   []Secret{secret},
				"map":     map[string]Secret{"k": secret},
			} {
				out := fmt.Sprintf(verb, value)
				require.NotContains(t, out, "hunter2", "verb %s, %s", verb, name)
				require.Contains(t, out, "redacted", "verb %s, %s", verb, name)
			}
		}
		require.NotContains(t, fmt.Sprint(secret), "hunter2")
		require.NotContains(t, fmt.Sprintln(secret), "hunter2")
	})

	t.Run("slog json handler", func(t *testing.T) {
		var buf bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&buf, nil))

		logger.Info("login", "password", secret)
		logger.Info("login", slog.Any("password", secret))
		logger.Info("login", slog.Group("account", slog.Any("password", secret)))
		logger.With("password", secret).Info("login")

		// slog resolves LogValue only on the attribute itself. A Secret inside
		// a struct, slice or map is encoded by encoding/json, which uses
		// MarshalText.
		logger.Info("login", "account", holder{Password: secret})
		logger.Info("login", "account", &holder{Password: secret})
		logger.Info("login", "passwords", []Secret{secret})
		logger.Info("login", "passwords", map[string]Secret{"k": secret})

		out := buf.String()
		require.NotContains(t, out, "hunter2")
		require.Equal(t, 8, strings.Count(out, `[redacted]`))
	})

	t.Run("encoding/json", func(t *testing.T) {
		for name, value := range map[string]any{
			"value":        secret,
			"pointer":      &secret,
			"struct field": holder{Password: secret},
			"slice":        []Secret{secret},
			"map value":    map[string]Secret{"k": secret},
			"nested":       map[string][]holder{"k": {{Password: secret}}},
		} {
			out, err := json.Marshal(value)
			require.NoError(t, err, name)
			require.NotContains(t, string(out), "hunter2", name)
			require.Contains(t, string(out), "[redacted]", name)
		}
	})

	t.Run("slog text handler", func(t *testing.T) {
		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, nil))

		logger.Info("login", "password", secret)
		logger.Info("login", "account", holder{Password: secret})
		logger.Info("login", "passwords", []Secret{secret})
		logger.Info("login", "passwords", map[string]Secret{"k": secret})

		require.NotContains(t, buf.String(), "hunter2")
		require.Equal(t, 4, strings.Count(buf.String(), "[redacted]"))
	})
}

// Redaction must only apply when a Secret is written out. Reading a value into
// a Secret (for example from a config file) must keep the real value.
func TestSecretDecodesFromJSON(t *testing.T) {
	var decoded holder
	require.NoError(t, json.Unmarshal([]byte(`{"Password":"hunter2"}`), &decoded))
	require.Equal(t, "hunter2", decoded.Password.Reveal())
}

// --- Keychain master key ---

func TestKeychainKEKCreatesAndReuses(t *testing.T) {
	keyring.MockInit()

	first, err := NewKeychainKEK()
	require.NoError(t, err)
	require.Equal(t, "keychain:v1", first.ID())

	// The first call stores a new key under the fixed service and user names.
	stored, err := keyring.Get("email-mcp", "master-key")
	require.NoError(t, err)
	key, err := base64.StdEncoding.DecodeString(stored)
	require.NoError(t, err)
	require.Len(t, key, 32)

	// The second call reads the same key and must not replace it.
	second, err := NewKeychainKEK()
	require.NoError(t, err)
	require.Equal(t, "keychain:v1", second.ID())
	stillStored, err := keyring.Get("email-mcp", "master-key")
	require.NoError(t, err)
	require.Equal(t, stored, stillStored)

	dek := randomBytes(t, 32)
	wrapped, err := first.Wrap(t.Context(), dek)
	require.NoError(t, err)
	require.NotContains(t, string(wrapped), string(dek))

	unwrapped, err := second.Unwrap(t.Context(), wrapped)
	require.NoError(t, err)
	require.Equal(t, dek, unwrapped)
}

func TestKeychainKEKWrapsWithFixedAAD(t *testing.T) {
	keyring.MockInit()

	kek, err := NewKeychainKEK()
	require.NoError(t, err)
	stored, err := keyring.Get("email-mcp", "master-key")
	require.NoError(t, err)
	key, err := base64.StdEncoding.DecodeString(stored)
	require.NoError(t, err)

	dek := randomBytes(t, 32)
	wrapped, err := kek.Wrap(t.Context(), dek)
	require.NoError(t, err)

	opened, err := Open(key, wrapped, "email-mcp/v1|kek")
	require.NoError(t, err)
	require.Equal(t, dek, opened)
}

func TestKeychainKEKCreatesDifferentKeysForDifferentKeychains(t *testing.T) {
	keyring.MockInit()
	first, err := NewKeychainKEK()
	require.NoError(t, err)

	keyring.MockInit() // a new, empty keychain
	second, err := NewKeychainKEK()
	require.NoError(t, err)

	wrapped, err := first.Wrap(t.Context(), randomBytes(t, 32))
	require.NoError(t, err)
	_, err = second.Unwrap(t.Context(), wrapped)
	require.ErrorIs(t, err, ErrDecrypt)
}

func TestKeychainKEKRejectsInvalidStoredKeyWithoutReplacingIt(t *testing.T) {
	sixteenBytes := base64.StdEncoding.EncodeToString(randomBytes(t, 16))
	thirtyThreeBytes := base64.StdEncoding.EncodeToString(randomBytes(t, 33))

	for name, value := range map[string]string{
		"not base64":        "not base64 at all!",
		"empty":             "",
		"16 bytes":          sixteenBytes,
		"33 bytes":          thirtyThreeBytes,
		"missing padding":   strings.TrimRight(base64.StdEncoding.EncodeToString(randomBytes(t, 32)), "="),
		"url-safe alphabet": strings.Repeat("-_", 21) + "A=",
	} {
		t.Run(name, func(t *testing.T) {
			keyring.MockInit()
			require.NoError(t, keyring.Set("email-mcp", "master-key", value))

			kek, err := NewKeychainKEK()
			require.Error(t, err)
			require.Nil(t, kek)
			if value != "" {
				require.NotContains(t, err.Error(), value, "the error must not repeat the stored value")
			}

			// An existing key must never be replaced.
			stored, err := keyring.Get("email-mcp", "master-key")
			require.NoError(t, err)
			require.Equal(t, value, stored)
		})
	}
}

func TestKeychainKEKReportsKeychainErrors(t *testing.T) {
	keyring.MockInitWithError(fmt.Errorf("keychain is locked"))
	t.Cleanup(keyring.MockInit)

	kek, err := NewKeychainKEK()
	require.ErrorContains(t, err, "keychain is locked")
	require.Nil(t, kek)
}

// --- Key file master key ---

func TestFileKEKRejectsLoosePermissions(t *testing.T) {
	skipOnWindows(t)

	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o660, 0o606, 0o666, 0o610, 0o601} {
		t.Run(fmt.Sprintf("%04o", mode), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "master.key")
			require.NoError(t, GenerateKeyFile(path))
			require.NoError(t, os.Chmod(path, mode))

			kek, err := NewFileKEK(path)
			require.ErrorIs(t, err, ErrKeyFilePermissions)
			require.Nil(t, kek)
		})
	}
}

func TestFileKEKAcceptsOwnerOnlyPermissions(t *testing.T) {
	skipOnWindows(t)

	key := base64.StdEncoding.EncodeToString(randomBytes(t, 32))
	for _, mode := range []os.FileMode{0o600, 0o400, 0o700} {
		t.Run(fmt.Sprintf("%04o", mode), func(t *testing.T) {
			kek, err := NewFileKEK(writeKeyFile(t, key, mode))
			require.NoError(t, err)
			require.NotNil(t, kek)
		})
	}
}

func TestFileKEKWrapsAndUnwrapsWithFixedAAD(t *testing.T) {
	rawKey := randomBytes(t, 32)
	path := writeKeyFile(t, base64.StdEncoding.EncodeToString(rawKey)+"\n", 0o600)

	kek, err := NewFileKEK(path)
	require.NoError(t, err)

	dek := randomBytes(t, 32)
	wrapped, err := kek.Wrap(t.Context(), dek)
	require.NoError(t, err)
	require.Len(t, wrapped, 12+32+16)

	unwrapped, err := kek.Unwrap(t.Context(), wrapped)
	require.NoError(t, err)
	require.Equal(t, dek, unwrapped)

	// The wrapped value is a normal Seal output with the fixed KEK AAD.
	opened, err := Open(rawKey, wrapped, "email-mcp/v1|kek")
	require.NoError(t, err)
	require.Equal(t, dek, opened)

	// A different KEK cannot unwrap it.
	other, err := NewFileKEK(writeKeyFile(t, base64.StdEncoding.EncodeToString(randomBytes(t, 32)), 0o600))
	require.NoError(t, err)
	_, err = other.Unwrap(t.Context(), wrapped)
	require.ErrorIs(t, err, ErrDecrypt)

	// A tampered value is rejected.
	wrapped[len(wrapped)-1] ^= 0x01
	_, err = kek.Unwrap(t.Context(), wrapped)
	require.ErrorIs(t, err, ErrDecrypt)
}

func TestFileKEKID(t *testing.T) {
	path := writeKeyFile(t, base64.StdEncoding.EncodeToString(randomBytes(t, 32)), 0o600)
	kek, err := NewFileKEK(path)
	require.NoError(t, err)

	sum := sha256.Sum256([]byte(path))
	require.Equal(t, "file:"+hex.EncodeToString(sum[:])[:8], kek.ID())
	require.Regexp(t, `^file:[0-9a-f]{8}$`, kek.ID())
}

func TestFileKEKIDUsesTheAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "master.key")
	require.NoError(t, GenerateKeyFile(keyPath))

	absolute, err := NewFileKEK(keyPath)
	require.NoError(t, err)

	t.Chdir(dir)
	relative, err := NewFileKEK("master.key")
	require.NoError(t, err)

	require.Equal(t, absolute.ID(), relative.ID())
}

func TestFileKEKAcceptsSurroundingWhitespace(t *testing.T) {
	rawKey := randomBytes(t, 32)
	encoded := base64.StdEncoding.EncodeToString(rawKey)

	for name, content := range map[string]string{
		"no whitespace":   encoded,
		"trailing LF":     encoded + "\n",
		"trailing CRLF":   encoded + "\r\n",
		"leading spaces":  "  \t" + encoded,
		"around the key":  "\n\n  " + encoded + " \t\r\n\n",
		"trailing spaces": encoded + "    ",
	} {
		t.Run(name, func(t *testing.T) {
			kek, err := NewFileKEK(writeKeyFile(t, content, 0o600))
			require.NoError(t, err)

			wrapped, err := kek.Wrap(t.Context(), []byte("dek"))
			require.NoError(t, err)
			opened, err := Open(rawKey, wrapped, "email-mcp/v1|kek")
			require.NoError(t, err)
			require.Equal(t, []byte("dek"), opened)
		})
	}
}

func TestFileKEKRejectsInvalidContent(t *testing.T) {
	thirtyTwoFB := bytes.Repeat([]byte{0xfb}, 32) // "+" and "/" in standard base64, "-" and "_" in URL-safe
	valid := base64.StdEncoding.EncodeToString(randomBytes(t, 32))

	for name, content := range map[string]string{
		"empty file":           "",
		"only whitespace":      " \n\t",
		"not base64":           "this is not base64 !!!",
		"16 bytes":             base64.StdEncoding.EncodeToString(randomBytes(t, 16)),
		"31 bytes":             base64.StdEncoding.EncodeToString(randomBytes(t, 31)),
		"33 bytes":             base64.StdEncoding.EncodeToString(randomBytes(t, 33)),
		"64 bytes":             base64.StdEncoding.EncodeToString(randomBytes(t, 64)),
		"no padding":           base64.RawStdEncoding.EncodeToString(randomBytes(t, 32)),
		"url-safe alphabet":    base64.URLEncoding.EncodeToString(thirtyTwoFB),
		"hex instead":          hex.EncodeToString(randomBytes(t, 32)),
		"split over two lines": valid[:22] + "\n" + valid[22:],
		"two keys":             valid + "\n" + valid,
		"key and a comment":    valid + " # master key",
		"very large file":      strings.Repeat("A", 1<<20),
	} {
		t.Run(name, func(t *testing.T) {
			kek, err := NewFileKEK(writeKeyFile(t, content, 0o600))
			require.Error(t, err)
			require.NotErrorIs(t, err, ErrKeyFilePermissions)
			require.Nil(t, kek)
			if strings.TrimSpace(content) != "" && len(content) < 1000 {
				require.NotContains(t, err.Error(), strings.TrimSpace(content),
					"the error must not repeat the file content")
			}
		})
	}
}

func TestFileKEKRejectsMissingFileAndDirectory(t *testing.T) {
	dir := t.TempDir()

	kek, err := NewFileKEK(filepath.Join(dir, "missing.key"))
	require.ErrorIs(t, err, fs.ErrNotExist)
	require.Nil(t, kek)

	kek, err = NewFileKEK(dir)
	require.Error(t, err)
	require.Nil(t, kek)
}

// --- Key file generation ---

func TestGenerateKeyFileCreatesUsableKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	require.NoError(t, GenerateKeyFile(path))

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}

	content := readFile(t, path)
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(content)))
	require.NoError(t, err)
	require.Len(t, key, 32)

	kek, err := NewFileKEK(path)
	require.NoError(t, err)
	wrapped, err := kek.Wrap(t.Context(), []byte("dek"))
	require.NoError(t, err)
	unwrapped, err := kek.Unwrap(t.Context(), wrapped)
	require.NoError(t, err)
	require.Equal(t, []byte("dek"), unwrapped)
}

func TestGenerateKeyFileCreatesDifferentKeys(t *testing.T) {
	dir := t.TempDir()
	first, second := filepath.Join(dir, "a.key"), filepath.Join(dir, "b.key")
	require.NoError(t, GenerateKeyFile(first))
	require.NoError(t, GenerateKeyFile(second))

	require.NotEqual(t, readFile(t, first), readFile(t, second))
}

func TestGenerateKeyFileRefusesToOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	require.NoError(t, GenerateKeyFile(path))
	before := readFile(t, path)

	err := GenerateKeyFile(path)
	require.ErrorIs(t, err, fs.ErrExist)

	require.Equal(t, before, readFile(t, path), "the existing key must not change")
}

func TestGenerateKeyFileRefusesToFollowASymlink(t *testing.T) {
	skipOnWindows(t)

	dir := t.TempDir()
	target := filepath.Join(dir, "target.key")
	link := filepath.Join(dir, "master.key")
	require.NoError(t, os.Symlink(target, link))

	err := GenerateKeyFile(link)
	require.ErrorIs(t, err, fs.ErrExist)
	require.NoFileExists(t, target)
}
