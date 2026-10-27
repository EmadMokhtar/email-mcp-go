// Package signing makes small tamper-proof tokens. The server gives a token to
// the AI agent, and the agent later sends it back. Examples are the page token
// of a search, the cursor of a new-message check, and the RequestState that
// proves a user approved one exact email.
//
// A token is integrity-protected, not secret. It is base64 text, so its
// content is readable by whoever holds it. Callers must not put secrets in a
// token.
//
// The signature stops three things:
//   - Changing a token. Any change breaks the signature.
//   - Making a token without the key.
//   - Using a token for another job. Every token is bound to a purpose, and
//     Open accepts it only for that same purpose.
//
// The signature does not stop replay. A valid token can be sent again until
// it expires, because a Signer keeps no record of the tokens it made. A caller
// that needs a token to work only once must keep that record itself.
package signing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The purposes that the server uses. A purpose is only a string, so other
// packages may pass their own purpose strings. This list is not closed.
const (
	// PurposeCursor is a new-message cursor.
	PurposeCursor = "cursor"
	// PurposePage is a search page token.
	PurposePage = "page"
	// PurposeRequestState is the confirmation state that proves a user
	// approved one exact email.
	PurposeRequestState = "request_state"
)

const (
	// minKeyLen is the smallest key that New accepts. It is the size of the
	// HMAC-SHA256 output, so a shorter key would be weaker than the hash.
	minKeyLen = 32

	// maxTokenLen is the longest token that Open reads. Seal never makes a
	// longer one. The limit stops a huge input from costing time and memory.
	maxTokenLen = 64 << 10
)

var (
	// ErrInvalid means the token cannot be trusted. It is malformed, it was
	// changed, it was made with another key, or it was made for another
	// purpose. Callers cannot tell these cases apart, on purpose.
	ErrInvalid = errors.New("signing: invalid token")

	// ErrExpired means the token is genuine and has the right purpose, but
	// its lifetime has ended. Open returns it only after the signature and
	// the purpose are correct, so it does not help someone who guesses.
	ErrExpired = errors.New("signing: token expired")
)

// Signer makes and opens tokens. Make one with New. A Signer is safe for
// concurrent use as long as nobody changes Now while it is in use.
type Signer struct {
	// Now returns the current time. When it is nil, time.Now is used. Tests
	// set it to control the clock.
	Now func() time.Time

	key []byte
}

// payload is the data inside a token, before base64 encoding.
type payload struct {
	P string          `json:"p"` // purpose
	E int64           `json:"e"` // expiry as Unix seconds, or 0 for no expiry
	D json.RawMessage `json:"d"` // the caller's value, as JSON
}

// New makes a Signer that uses key. It copies the key, so the caller may
// change or clear its own slice afterwards.
//
// The key must be at least 32 bytes. A shorter key is a bug in the program,
// not bad input, so New panics.
func New(key []byte) *Signer {
	if len(key) < minKeyLen {
		panic("signing: key must be at least 32 bytes")
	}
	return &Signer{key: append([]byte(nil), key...)}
}

// Seal returns a token that holds v and is bound to purpose. The token works
// with Open for the same purpose, until ttl has passed. A ttl of 0 means the
// token never expires. A negative ttl is an error.
//
// The expiry is stored in whole seconds, rounded down. Use a ttl of at least
// one second.
//
// v is written as JSON, and anyone who holds the token can read it. Do not
// put secrets in v.
func (s *Signer) Seal(purpose string, v any, ttl time.Duration) (string, error) {
	if len(s.key) < minKeyLen {
		return "", errors.New("signing: signer has no key, make it with New")
	}
	if ttl < 0 {
		return "", errors.New("signing: ttl must not be negative")
	}

	data, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("signing: encode value: %w", err)
	}

	var expiry int64
	if ttl > 0 {
		expiry = s.now().Add(ttl).Unix()
	}
	body, err := json.Marshal(payload{P: purpose, E: expiry, D: data})
	if err != nil {
		return "", fmt.Errorf("signing: encode payload: %w", err)
	}

	encoded := base64.RawURLEncoding.EncodeToString(body)
	token := encoded + "." + s.sign(encoded)
	if len(token) > maxTokenLen {
		return "", fmt.Errorf("signing: token is %d bytes, the limit is %d", len(token), maxTokenLen)
	}
	return token, nil
}

// Open checks token and decodes its value into v. It accepts the token only if
// the token is genuine, was made for purpose, and has not expired.
//
// It returns ErrInvalid or ErrExpired. It never panics on bad input. When it
// returns an error, do not use v: it may be filled in part.
//
// If v is nil, Open checks the token and does not decode the value. If v is
// not a pointer, Open returns an error that is neither ErrInvalid nor
// ErrExpired, because that is a bug in the caller and not a bad token.
func (s *Signer) Open(purpose, token string, v any) error {
	if len(s.key) < minKeyLen || len(token) > maxTokenLen {
		return ErrInvalid
	}

	// The encoded payload never has a dot, so the last dot ends it.
	dot := strings.LastIndexByte(token, '.')
	if dot < 0 {
		return ErrInvalid
	}
	encoded, signature := token[:dot], token[dot+1:]

	// Check the signature first. Nothing from an unverified token is decoded.
	// The two encoded signatures are compared as text, so each MAC has exactly
	// one accepted spelling.
	if !hmac.Equal([]byte(signature), []byte(s.sign(encoded))) {
		return ErrInvalid
	}

	body, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return ErrInvalid
	}
	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		return ErrInvalid
	}

	if p.P != purpose {
		return ErrInvalid
	}
	if p.E != 0 && !s.now().Before(time.Unix(p.E, 0)) {
		return ErrExpired
	}

	if v == nil {
		return nil
	}
	if err := json.Unmarshal(p.D, v); err != nil {
		var badTarget *json.InvalidUnmarshalError
		if errors.As(err, &badTarget) {
			return fmt.Errorf("signing: %w", err)
		}
		return ErrInvalid
	}
	return nil
}

// sign returns the base64url HMAC-SHA256 of the encoded payload.
func (s *Signer) sign(encodedPayload string) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(encodedPayload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// now returns the time from Now, or the real time when Now is nil.
func (s *Signer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
