package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/Sparkzure/PayPrint/server/internal/domain"
)

const SignScheme = "payprint/v1"

const p256CoordSize = 32

func CanonicalString(method, path, rawQuery string, timestamp int64, nonce, bodyHashHex string) string {
	return strings.Join([]string{
		SignScheme,
		strings.ToUpper(method),
		path,
		CanonicalQuery(rawQuery),
		strconv.FormatInt(timestamp, 10),
		nonce,
		strings.ToLower(bodyHashHex),
	}, "\n")
}

func BodyHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func CanonicalQuery(rawQuery string) string {
	rawQuery = strings.TrimPrefix(strings.TrimSpace(rawQuery), "?")
	if rawQuery == "" {
		return ""
	}

	values, err := url.ParseQuery(rawQuery)
	if err != nil {

		return rawQuery
	}

	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		vs := values[k]
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

func ParsePublicKey(encoded string) (*ecdsa.PublicKey, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, domain.ErrMissingSignature.WithMessage("设备未提供公钥")
	}

	der, ok := DecodeBase64Any(encoded)
	if !ok {
		if block, _ := pem.Decode([]byte(encoded)); block != nil {
			der = block.Bytes
		} else {
			return nil, domain.ErrInvalidPublicKey.WithErr(fmt.Errorf("无法解码 base64"))
		}
	}

	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, domain.ErrInvalidPublicKey.WithErr(err)
	}
	pub, ok := parsed.(*ecdsa.PublicKey)
	if !ok {
		return nil, domain.ErrInvalidPublicKey.WithErr(fmt.Errorf("公钥不是 ECDSA"))
	}
	if pub.Curve != elliptic.P256() {
		return nil, domain.ErrInvalidPublicKey.WithErr(
			fmt.Errorf("仅支持 P-256，收到 %s", pub.Curve.Params().Name))
	}
	return pub, nil
}

func EncodePublicKey(pub *ecdsa.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("编码公钥: %w", err)
	}
	return encodeBase64Std(der), nil
}

func VerifySignature(pub *ecdsa.PublicKey, message []byte, signatureEncoded string) error {
	if pub == nil {
		return domain.ErrInvalidPublicKey
	}
	sig, ok := DecodeBase64Any(signatureEncoded)
	if !ok || len(sig) == 0 {
		return domain.ErrMissingSignature.WithMessage("设备签名无法解码")
	}

	digest := sha256.Sum256(message)

	if len(sig) == 2*p256CoordSize {
		r := new(big.Int).SetBytes(sig[:p256CoordSize])
		s := new(big.Int).SetBytes(sig[p256CoordSize:])
		if ecdsa.Verify(pub, digest[:], r, s) {
			return nil
		}
		return domain.ErrInvalidSignature
	}

	if ecdsa.VerifyASN1(pub, digest[:], sig) {
		return nil
	}
	return domain.ErrInvalidSignature
}

func SignForTest(priv *ecdsa.PrivateKey, message []byte) (string, error) {
	digest := sha256.Sum256(message)
	r, s, err := ecdsa.Sign(randReader(), priv, digest[:])
	if err != nil {
		return "", fmt.Errorf("签名: %w", err)
	}
	out := make([]byte, 2*p256CoordSize)
	r.FillBytes(out[:p256CoordSize])
	s.FillBytes(out[p256CoordSize:])
	return encodeBase64Std(out), nil
}
