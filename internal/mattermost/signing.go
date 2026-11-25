package mattermost

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Signer adds and checks an HMAC over button contexts and dialog state, so
// nobody can forge a click payload (e.g. change the action or the user).
type Signer struct {
	key    []byte
	maxAge time.Duration
	now    func() time.Time
}

const (
	sigField = "sig"
	tsField  = "ts"
)

// ErrBadSignature means the payload was tampered with, expired or unsigned.
var ErrBadSignature = errors.New("invalid or expired signature")

// NewSigner returns a Signer; signed values are accepted for maxAge.
func NewSigner(key string, maxAge time.Duration) *Signer {
	return &Signer{key: []byte(key), maxAge: maxAge, now: time.Now}
}

// Sign returns a copy of fields with "ts" and "sig" added.
func (s *Signer) Sign(fields map[string]string) map[string]string {
	out := make(map[string]string, len(fields)+2)
	for k, v := range fields {
		out[k] = v
	}
	out[tsField] = strconv.FormatInt(s.now().Unix(), 10)
	delete(out, sigField)
	out[sigField] = s.mac(out)
	return out
}

// Verify checks the signature and age. It returns the fields without "sig".
func (s *Signer) Verify(fields map[string]string) (map[string]string, error) {
	sig, ok := fields[sigField]
	if !ok {
		return nil, ErrBadSignature
	}
	rest := make(map[string]string, len(fields))
	for k, v := range fields {
		if k != sigField {
			rest[k] = v
		}
	}
	if !hmac.Equal([]byte(sig), []byte(s.mac(rest))) {
		return nil, ErrBadSignature
	}
	ts, err := strconv.ParseInt(rest[tsField], 10, 64)
	if err != nil || s.now().Sub(time.Unix(ts, 0)) > s.maxAge {
		return nil, ErrBadSignature
	}
	return rest, nil
}

// mac covers every field in sorted key order; keys and values are
// length-prefixed so "a=bc" and "ab=c" can't collide.
func (s *Signer) mac(fields map[string]string) string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(strconv.Itoa(len(k)) + ":" + k + strconv.Itoa(len(fields[k])) + ":" + fields[k])
	}
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(b.String()))
	return hex.EncodeToString(m.Sum(nil))
}
