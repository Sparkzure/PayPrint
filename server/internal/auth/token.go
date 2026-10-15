package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"

	"github.com/Sparkzure/PayPrint/server/internal/ids"
)

const sessionTokenBytes = 32

func NewSessionToken() (token string, hash string, err error) {
	token, err = ids.NewRandom(sessionTokenBytes)
	if err != nil {
		return "", "", err
	}
	return token, HashToken(token), nil
}

func NewAPIToken() (token string, hash string, err error) {
	return NewSessionToken()
}

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func NewNonce() (string, error) {
	return ids.NewRandom(24)
}

func DecodeBase64Any(s string) ([]byte, bool) {
	s = trimBase64(s)
	if s == "" {
		return nil, false
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, true
		}
	}
	return nil, false
}

func trimBase64(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ', '\n', '\r', '\t', '"':
			continue
		default:
			out = append(out, s[i])
		}
	}
	return string(out)
}
