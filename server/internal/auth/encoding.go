package auth

import (
	"crypto/rand"
	"encoding/base64"
	"io"
)

func randReader() io.Reader { return rand.Reader }

func encodeBase64Std(b []byte) string {
	return base64.RawStdEncoding.EncodeToString(b)
}
