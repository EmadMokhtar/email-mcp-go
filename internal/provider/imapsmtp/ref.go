package imapsmtp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
)

// maxRefLen is the longest ref that decodeRef reads. A folder name is short,
// so a longer ref is not one that this backend made.
const maxRefLen = 4096

// imapRef is what a mail.Ref holds: the message is the UID in one folder, and
// the UIDVALIDITY tells whether the UIDs of that folder are still the same.
// When the UIDVALIDITY of the folder changes, the UID may name another
// message, so a ref with the old UIDVALIDITY is stale.
//
// A ref is not signed. It names a message in a folder of the account that the
// backend is logged in to, and the account may read all of them anyway.
type imapRef struct {
	Mailbox     string `json:"m"`
	UIDValidity uint32 `json:"v"`
	UID         uint32 `json:"u"`
}

// encodeRef returns the ref of r: JSON, in base64url without padding.
func encodeRef(r imapRef) mail.Ref {
	data, err := json.Marshal(r)
	if err != nil {
		// A string and two numbers always encode, so this cannot happen.
		return ""
	}
	return mail.Ref(base64.RawURLEncoding.EncodeToString(data))
}

// decodeRef reads a ref. Anything that encodeRef could not have made gives an
// error that wraps mail.ErrNotFound.
func decodeRef(ref mail.Ref) (imapRef, error) {
	bad := func(why string) (imapRef, error) {
		return imapRef{}, fmt.Errorf("%w: the message reference is not valid (%s)", mail.ErrNotFound, why)
	}
	if ref == "" || len(ref) > maxRefLen {
		return bad("wrong length")
	}
	data, err := base64.RawURLEncoding.DecodeString(string(ref))
	if err != nil {
		return bad("not base64url")
	}
	var r imapRef
	if err := json.Unmarshal(data, &r); err != nil {
		return bad("not the expected JSON")
	}
	if r.Mailbox == "" || r.UID == 0 {
		return bad("no folder or no UID")
	}
	// RFC 3501 says UIDVALIDITY is never zero. The backend never makes a ref
	// with zero, so a ref with zero was not made by it.
	if r.UIDValidity == 0 {
		return bad("no UIDVALIDITY")
	}
	return r, nil
}
