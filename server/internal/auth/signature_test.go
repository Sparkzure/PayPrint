package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"strings"
	"testing"
)

func TestCanonicalQueryIsOrderIndependent(t *testing.T) {

	a := CanonicalQuery("b=2&a=1&a=0")
	b := CanonicalQuery("a=0&b=2&a=1")
	if a != b {
		t.Fatalf("规范化结果应当一致：%q vs %q", a, b)
	}
	if a != "a=0&a=1&b=2" {
		t.Fatalf("规范化结果不正确: %q", a)
	}
	if CanonicalQuery("") != "" || CanonicalQuery("?") != "" {
		t.Fatal("空 query 应当规范化为空串")
	}
}

func TestCanonicalStringCoversEverything(t *testing.T) {
	base := CanonicalString("post", "/api/v1/orders", "", 1700000000, "nonce1", BodyHash(nil))

	variants := map[string]string{
		"method": CanonicalString("GET", "/api/v1/orders", "", 1700000000, "nonce1", BodyHash(nil)),
		"path":   CanonicalString("POST", "/api/v1/orders/2", "", 1700000000, "nonce1", BodyHash(nil)),
		"query":  CanonicalString("POST", "/api/v1/orders", "uid=2", 1700000000, "nonce1", BodyHash(nil)),
		"ts":     CanonicalString("POST", "/api/v1/orders", "", 1700000001, "nonce1", BodyHash(nil)),
		"nonce":  CanonicalString("POST", "/api/v1/orders", "", 1700000000, "nonce2", BodyHash(nil)),
		"body":   CanonicalString("POST", "/api/v1/orders", "", 1700000000, "nonce1", BodyHash([]byte("x"))),
	}
	for name, got := range variants {
		if got == base {
			t.Errorf("%s 变化后签名明文没有变化", name)
		}
	}

	if CanonicalString("post", "/a", "", 1, "n", "h") != CanonicalString("POST", "/a", "", 1, "n", "h") {
		t.Error("method 大小写应当被归一化")
	}
	if strings.Count(base, "\n") != 6 {
		t.Errorf("明文应为 7 行（6 个换行），实际 %d", strings.Count(base, "\n"))
	}
}

func TestSignAndVerifyRoundTrip(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成密钥: %v", err)
	}

	encoded, err := EncodePublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("编码公钥: %v", err)
	}
	parsed, err := ParsePublicKey(encoded)
	if err != nil {
		t.Fatalf("解析公钥: %v", err)
	}

	message := []byte("payprint/v1\nPOST\n/api/v1/orders\n\n1700000000\nnonce\nhash")
	sig, err := SignForTest(priv, message)
	if err != nil {
		t.Fatalf("签名: %v", err)
	}

	if err := VerifySignature(parsed, message, sig); err != nil {
		t.Fatalf("验签应当通过: %v", err)
	}

	tampered := []byte(string(message) + "x")
	if err := VerifySignature(parsed, tampered, sig); err == nil {
		t.Fatal("被篡改的明文不应通过验签")
	}

	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err := VerifySignature(&other.PublicKey, message, sig); err == nil {
		t.Fatal("用其他公钥验签不应通过")
	}

	if err := VerifySignature(parsed, message, "!!!not-base64!!!"); err == nil {
		t.Fatal("无法解码的签名不应通过")
	}
}

func TestParsePublicKeyRejectsNonP256(t *testing.T) {
	priv, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	encoded, err := EncodePublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("编码公钥: %v", err)
	}
	if _, err := ParsePublicKey(encoded); err == nil {
		t.Fatal("P-384 公钥应当被拒绝")
	}
}

func TestPasswordHashAndVerify(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("生成散列: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("散列应当自带算法与参数描述，实际: %q", hash)
	}
	if err := VerifyPassword(hash, "correct horse battery staple"); err != nil {
		t.Fatalf("正确密码应当通过: %v", err)
	}
	if err := VerifyPassword(hash, "wrong password"); err == nil {
		t.Fatal("错误密码不应通过")
	}

	hash2, _ := HashPassword("correct horse battery staple")
	if hash == hash2 {
		t.Fatal("相同密码的两次散列不应相同")
	}
}

func TestSessionTokenHash(t *testing.T) {
	token, hash, err := NewSessionToken()
	if err != nil {
		t.Fatalf("生成令牌: %v", err)
	}
	if token == hash {
		t.Fatal("令牌不应与其散列相同")
	}
	if HashToken(token) != hash {
		t.Fatal("令牌散列不稳定")
	}
	if len(hash) != 64 {
		t.Fatalf("SHA-256 十六进制应为 64 字符，实际 %d", len(hash))
	}
}
