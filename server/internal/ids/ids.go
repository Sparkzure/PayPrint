package ids

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const orderNoAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"

func New() string {
	return uuid.NewString()
}

func NewRandom(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成随机数: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func NewOrderNo(now time.Time) (string, error) {
	suffix, err := randomAlnum(4)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("OC%s%s", now.UTC().Format("20060102150405"), suffix), nil
}

func randomAlnum(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成随机码: %w", err)
	}
	var sb strings.Builder
	for _, b := range buf {
		sb.WriteByte(orderNoAlphabet[int(b)%len(orderNoAlphabet)])
	}
	return sb.String(), nil
}
