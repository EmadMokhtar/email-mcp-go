package signing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// --- helpers ---

// testState is a value shaped like the real tokens: a few small fields.
type testState struct {
	Folder string `json:"folder"`
	UID    uint32 `json:"uid"`
	Total  int    `json:"total"`
}

var sampleState = testState{Folder: "INBOX", UID: 4242, Total: 17}

// baseTime is a whole second, so expiry arithmetic in the tests is exact.
var baseTime = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

// testKey returns a 32-byte key filled with b. Different b gives a different key.
func testKey(b byte) []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = b
	}
	return key
}

// clock is a controllable time source for Signer.Now.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

// newTestSigner returns a Signer that uses key and a clock fixed at baseTime.
func newTestSigner(key []byte) (*Signer, *clock) {
	c := &clock{now: baseTime}
	s := New(key)
	s.Now = c.Now
	return s, c
}

// signEncoded builds a token from an already-encoded payload string, with a
// correct MAC. It does not use the code under test, so tests can build tokens
// that are correctly signed but have a bad payload.
func signEncoded(key []byte, encodedPayload string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(encodedPayload))
	return encodedPayload + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// signBody builds a correctly signed token whose payload decodes to body.
func signBody(key []byte, body string) string {
	return signEncoded(key, base64.RawURLEncoding.EncodeToString([]byte(body)))
}

func seal(t *testing.T, s *Signer, purpose string, v any, ttl time.Duration) string {
	t.Helper()
	token, err := s.Seal(purpose, v, ttl)
	require.NoError(t, err)
	return token
}

// --- tests ---

func TestSealOpenRoundTrip(t *testing.T) {
	purposes := []string{
		PurposeCursor,
		PurposePage,
		PurposeRequestState,
		"csrf", // the purpose constants are not a closed set
	}
	tokenShape := regexp.MustCompile(`^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)

	for _, purpose := range purposes {
		t.Run(purpose, func(t *testing.T) {
			s, _ := newTestSigner(testKey(1))

			token := seal(t, s, purpose, sampleState, 10*time.Minute)
			require.Regexp(t, tokenShape, token, "a token uses only URL-safe characters")

			var got testState
			require.NoError(t, s.Open(purpose, token, &got))
			require.Equal(t, sampleState, got)
		})
	}
}

func TestSealOpenAnyJSONValue(t *testing.T) {
	s, _ := newTestSigner(testKey(1))

	var gotString string
	require.NoError(t, s.Open(PurposeCursor, seal(t, s, PurposeCursor, "after-uid-9", 0), &gotString))
	require.Equal(t, "after-uid-9", gotString)

	var gotMap map[string]int
	require.NoError(t, s.Open(PurposeCursor, seal(t, s, PurposeCursor, map[string]int{"a": 1}, 0), &gotMap))
	require.Equal(t, map[string]int{"a": 1}, gotMap)
}

func TestSealOpenNoExpiry(t *testing.T) {
	s, c := newTestSigner(testKey(1))

	token := seal(t, s, PurposeCursor, sampleState, 0)

	c.now = baseTime.AddDate(100, 0, 0)
	var got testState
	require.NoError(t, s.Open(PurposeCursor, token, &got), "ttl 0 means the token never expires")
	require.Equal(t, sampleState, got)
}

func TestOpenWrongPurposeInvalid(t *testing.T) {
	s, _ := newTestSigner(testKey(1))
	token := seal(t, s, PurposeCursor, sampleState, 10*time.Minute)

	var got testState
	err := s.Open(PurposeRequestState, token, &got)
	require.ErrorIs(t, err, ErrInvalid)
	require.NotErrorIs(t, err, ErrExpired)
	require.Zero(t, got, "a rejected token must not fill the value")
}

func TestOpenWrongPurposeBeatsExpiry(t *testing.T) {
	s, c := newTestSigner(testKey(1))
	token := seal(t, s, PurposeCursor, sampleState, time.Minute)
	c.now = baseTime.Add(time.Hour)

	var got testState
	err := s.Open(PurposeRequestState, token, &got)
	require.ErrorIs(t, err, ErrInvalid, "the purpose is checked before the expiry")
	require.NotErrorIs(t, err, ErrExpired)
}

func TestOpenTamperedInvalid(t *testing.T) {
	s, _ := newTestSigner(testKey(1))
	token := seal(t, s, PurposeRequestState, sampleState, 10*time.Minute)
	other := seal(t, s, PurposeRequestState, testState{Folder: "Sent", UID: 1, Total: 1}, 10*time.Minute)

	open := func(tok string) error {
		var got testState
		return s.Open(PurposeRequestState, tok, &got)
	}

	t.Run("change any single character", func(t *testing.T) {
		for i := 0; i < len(token); i++ {
			replacement := byte('A')
			if token[i] == 'A' {
				replacement = 'B'
			}
			mutated := token[:i] + string(replacement) + token[i+1:]
			require.ErrorIs(t, open(mutated), ErrInvalid, "character %d", i)
		}
	})

	t.Run("truncate at any length", func(t *testing.T) {
		for i := 0; i < len(token); i++ {
			require.ErrorIs(t, open(token[:i]), ErrInvalid, "length %d", i)
		}
	})

	t.Run("add characters", func(t *testing.T) {
		require.ErrorIs(t, open(token+"A"), ErrInvalid)
		require.ErrorIs(t, open("A"+token), ErrInvalid)
		require.ErrorIs(t, open(token+".AAAA"), ErrInvalid)
	})

	t.Run("payload of one token with the signature of another", func(t *testing.T) {
		payloadOfToken := token[:strings.LastIndexByte(token, '.')]
		sigOfOther := other[strings.LastIndexByte(other, '.')+1:]
		require.ErrorIs(t, open(payloadOfToken+"."+sigOfOther), ErrInvalid)
	})

	t.Run("unchanged token still opens", func(t *testing.T) {
		require.NoError(t, open(token))
	})
}

func TestOpenExpired(t *testing.T) {
	const ttl = 10 * time.Minute

	t.Run("one second after the ttl", func(t *testing.T) {
		s, c := newTestSigner(testKey(1))
		token := seal(t, s, PurposeRequestState, sampleState, ttl)

		c.now = baseTime.Add(ttl + time.Second)
		var got testState
		require.ErrorIs(t, s.Open(PurposeRequestState, token, &got), ErrExpired)
		require.Zero(t, got, "an expired token must not fill the value")
	})

	t.Run("at the exact expiry time", func(t *testing.T) {
		s, c := newTestSigner(testKey(1))
		token := seal(t, s, PurposeRequestState, sampleState, ttl)

		c.now = baseTime.Add(ttl)
		var got testState
		require.ErrorIs(t, s.Open(PurposeRequestState, token, &got), ErrExpired)
	})

	t.Run("one second before the expiry time", func(t *testing.T) {
		s, c := newTestSigner(testKey(1))
		token := seal(t, s, PurposeRequestState, sampleState, ttl)

		c.now = baseTime.Add(ttl - time.Second)
		var got testState
		require.NoError(t, s.Open(PurposeRequestState, token, &got))
		require.Equal(t, sampleState, got)
	})
}

func TestNowDefaultsToTimeNow(t *testing.T) {
	// The token was sealed an hour ago with a one-minute lifetime. A Signer
	// whose Now is nil must use the real time, so it sees the token as expired.
	s, c := newTestSigner(testKey(1))
	c.now = time.Now().Add(-time.Hour)
	token := seal(t, s, PurposePage, sampleState, time.Minute)

	s.Now = nil
	var got testState
	require.ErrorIs(t, s.Open(PurposePage, token, &got), ErrExpired)

	// A token that is still valid in real time opens with the nil clock.
	fresh := seal(t, s, PurposePage, sampleState, time.Hour)
	require.NoError(t, s.Open(PurposePage, fresh, &got))
	require.Equal(t, sampleState, got)
}

func TestDifferentKeyInvalid(t *testing.T) {
	signer, _ := newTestSigner(testKey(1))
	other, _ := newTestSigner(testKey(2))
	token := seal(t, signer, PurposeCursor, sampleState, 10*time.Minute)

	var got testState
	require.ErrorIs(t, other.Open(PurposeCursor, token, &got), ErrInvalid)
	require.Zero(t, got)
}

func TestOpenMalformedInvalid(t *testing.T) {
	key := testKey(1)
	s, _ := newTestSigner(key)

	tests := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"only a dot", "."},
		{"no dot", "abcdef"},
		{"empty payload and signature", "..."},
		{"two parts, not a token", "abc.def"},
		{"not base64 in a signature", "abc.!!!"},
		{"signed empty string", signEncoded(key, "")},
		{"signed payload that is not base64", signEncoded(key, "!!!not-base64!!!")},
		{"signed payload with padding", signEncoded(key, "e30=")},
		{"signed payload that is not JSON", signBody(key, "not json")},
		{"signed payload that is empty JSON object", signBody(key, "{}")},
		{"signed payload that is JSON null", signBody(key, "null")},
		{"signed payload that is a JSON array", signBody(key, "[]")},
		{"signed payload without purpose", signBody(key, `{"e":0,"d":{}}`)},
		{"signed payload without data", signBody(key, `{"p":"cursor","e":0}`)},
		{"signed payload with a string expiry", signBody(key, `{"p":"cursor","e":"x","d":{}}`)},
		{"signed payload with data of the wrong type", signBody(key, `{"p":"cursor","e":0,"d":"text"}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got testState
			require.ErrorIs(t, s.Open(PurposeCursor, tt.token, &got), ErrInvalid)
		})
	}
}

func TestOpenValidTokenWithWrongShapeInvalid(t *testing.T) {
	// The value is signed and has the right purpose, but it does not fit the
	// type that the caller gave. This is a bad token, not a program error.
	s, _ := newTestSigner(testKey(1))
	token := seal(t, s, PurposeCursor, "just a string", 0)

	var got testState
	require.ErrorIs(t, s.Open(PurposeCursor, token, &got), ErrInvalid)
}

func TestOpenValueMustBePointer(t *testing.T) {
	// Passing a value that json cannot decode into is a bug in the caller. It
	// must not look like an invalid token.
	s, _ := newTestSigner(testKey(1))
	token := seal(t, s, PurposeCursor, sampleState, 0)

	err := s.Open(PurposeCursor, token, sampleState)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrInvalid)
	require.NotErrorIs(t, err, ErrExpired)
}

func TestOpenNilValueChecksTokenOnly(t *testing.T) {
	s, c := newTestSigner(testKey(1))
	token := seal(t, s, PurposeCursor, sampleState, time.Minute)

	require.NoError(t, s.Open(PurposeCursor, token, nil))
	require.ErrorIs(t, s.Open(PurposePage, token, nil), ErrInvalid)

	c.now = baseTime.Add(time.Hour)
	require.ErrorIs(t, s.Open(PurposeCursor, token, nil), ErrExpired)
}

func TestTokenSizeLimit(t *testing.T) {
	key := testKey(1)
	s, _ := newTestSigner(key)

	t.Run("seal refuses a value that makes a token over 64 KiB", func(t *testing.T) {
		_, err := s.Seal(PurposePage, strings.Repeat("a", maxTokenLen), 0)
		require.Error(t, err)
	})

	t.Run("seal accepts a token near the limit and open reads it", func(t *testing.T) {
		// Base64 grows the payload by 4/3, so this payload gives a token that
		// is close to, but below, the limit.
		value := strings.Repeat("a", maxTokenLen*3/4-200)
		token := seal(t, s, PurposePage, value, 0)
		require.LessOrEqual(t, len(token), maxTokenLen)

		var got string
		require.NoError(t, s.Open(PurposePage, token, &got))
		require.Equal(t, value, got)
	})

	t.Run("open refuses a correctly signed token over 64 KiB", func(t *testing.T) {
		body := fmt.Sprintf(`{"p":"page","e":0,"d":%q}`, strings.Repeat("a", maxTokenLen))
		token := signBody(key, body)
		require.Greater(t, len(token), maxTokenLen)

		var got string
		require.ErrorIs(t, s.Open(PurposePage, token, &got), ErrInvalid)

		// The same shape under the limit is accepted, so the size check is
		// the only reason for the refusal above.
		small := signBody(key, `{"p":"page","e":0,"d":"aaa"}`)
		require.NoError(t, s.Open(PurposePage, small, &got))
		require.Equal(t, "aaa", got)
	})
}

func TestSealErrors(t *testing.T) {
	s, _ := newTestSigner(testKey(1))

	t.Run("value that json cannot encode", func(t *testing.T) {
		_, err := s.Seal(PurposeCursor, make(chan int), 0)
		require.Error(t, err)
	})

	t.Run("negative ttl", func(t *testing.T) {
		_, err := s.Seal(PurposeCursor, sampleState, -time.Second)
		require.Error(t, err)
	})
}

func TestNewCopiesKey(t *testing.T) {
	key := testKey(7)
	s := New(key)
	token := seal(t, s, PurposeCursor, sampleState, 0)

	// Changing the caller's slice after New must not change the Signer.
	for i := range key {
		key[i] = 0
	}

	var got testState
	require.NoError(t, s.Open(PurposeCursor, token, &got))
	require.Equal(t, sampleState, got)

	// And a token made with the changed key is not accepted.
	forged := signBody(key, `{"p":"cursor","e":0,"d":{}}`)
	require.ErrorIs(t, s.Open(PurposeCursor, forged, &got), ErrInvalid)
}

func TestNewShortKeyPanics(t *testing.T) {
	for _, n := range []int{0, 1, 31} {
		t.Run(fmt.Sprintf("%d bytes", n), func(t *testing.T) {
			require.PanicsWithValue(t,
				"signing: key must be at least 32 bytes",
				func() { New(make([]byte, n)) })
		})
	}

	require.NotPanics(t, func() { New(make([]byte, 32)) })
	require.NotPanics(t, func() { New(make([]byte, 64)) })
}

func TestZeroSignerFailsClosed(t *testing.T) {
	// A Signer that was not made by New has no key. It must not sign with an
	// empty key, because anyone could forge such a token.
	var zero Signer

	_, err := zero.Seal(PurposeCursor, sampleState, 0)
	require.Error(t, err)

	forged := signBody(nil, `{"p":"cursor","e":0,"d":{}}`)
	var got testState
	require.ErrorIs(t, zero.Open(PurposeCursor, forged, &got), ErrInvalid)
}

// --- fuzz tests ---

// checkOpenResult fails the test when Open returns an error that is not one
// of the documented results.
func checkOpenResult(t *testing.T, err error) {
	t.Helper()
	if err != nil && !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrExpired) {
		t.Fatalf("unexpected error from Open: %v", err)
	}
}

// FuzzOpen gives Open arbitrary text. It must not panic, and it may return
// only nil, ErrInvalid or ErrExpired.
func FuzzOpen(f *testing.F) {
	key := testKey(1)
	s, _ := newTestSigner(key)
	valid, err := s.Seal(PurposePage, sampleState, time.Hour)
	require.NoError(f, err)

	for _, seed := range []string{
		"",
		".",
		"..",
		valid,
		valid[:len(valid)/2],
		valid + ".",
		"." + valid,
		strings.Repeat("A", 100) + "." + strings.Repeat("B", 43),
		signBody(key, `{"p":"page","e":0,"d":{}}`),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, token string) {
		var got testState
		checkOpenResult(t, s.Open(PurposePage, token, &got))
		checkOpenResult(t, s.Open(PurposePage, token, nil))
	})
}

// FuzzOpenSigned wraps arbitrary text in a token with a correct MAC, so the
// checks that come after the MAC check also get arbitrary input.
func FuzzOpenSigned(f *testing.F) {
	key := testKey(1)
	s, _ := newTestSigner(key)

	for _, seed := range []string{
		"",
		"{}",
		"null",
		`{"p":"page","e":0,"d":{}}`,
		`{"p":"page","e":1,"d":{"folder":"INBOX","uid":1,"total":2}}`,
		`{"p":"page","e":-9223372036854775808,"d":null}`,
		`{"p":"page","e":9223372036854775807,"d":"x"}`,
		`{"p":"page","e":0,"d":[1,2,3]}`,
		"\xff\xfe",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, body string) {
		var got testState
		// The text as the JSON payload, correctly encoded.
		checkOpenResult(t, s.Open(PurposePage, signBody(key, body), &got))
		// The text as the encoded payload itself, usually not valid base64.
		checkOpenResult(t, s.Open(PurposePage, signEncoded(key, body), &got))
	})
}
