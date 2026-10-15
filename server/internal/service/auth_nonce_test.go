package service

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"testing"

	"github.com/Sparkzure/PayPrint/server/internal/auth"
	"github.com/Sparkzure/PayPrint/server/internal/domain"
)

func TestFailedRegistrationStillConsumesNonce(t *testing.T) {
	e := newPaymentTestEnv(t)

	e.createUser("taken-user", domain.RoleUser)

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成设备密钥: %v", err)
	}
	encoded, err := auth.EncodePublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("编码设备公钥: %v", err)
	}

	const (
		fingerprint = "fp-failed-register"
		nonce       = "nonce-00000000000000a1"
	)
	body := []byte(`{"username":"taken-user","password":"password-123"}`)
	timestamp := e.now.Unix()

	in := DeviceAuthInput{
		Method:      "POST",
		Path:        "/api/v1/auth/register",
		Body:        body,
		Fingerprint: fingerprint,
		Timestamp:   timestamp,
		Nonce:       nonce,
		PublicKey:   encoded,
	}
	canonical := auth.CanonicalString(in.Method, in.Path, "", timestamp, nonce, auth.BodyHash(body))
	signature, err := auth.SignForTest(priv, []byte(canonical))
	if err != nil {
		t.Fatalf("签名: %v", err)
	}
	in.Signature = signature

	_, err = e.svc.Auth.Register(e.ctx(), RegisterInput{
		Username: "taken-user",
		Password: "password-123",
		Device:   in,
	})
	requireErrorCode(t, err, "username_taken")

	_, err = e.svc.Auth.Register(e.ctx(), RegisterInput{
		Username: "taken-user",
		Password: "password-123",
		Device:   in,
	})
	requireErrorCode(t, err, "nonce_replayed")
}

func TestRegistrationSucceedsWithFreshNonce(t *testing.T) {
	e := newPaymentTestEnv(t)

	e.createUser("placeholder", domain.RoleUser)

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成设备密钥: %v", err)
	}
	encoded, err := auth.EncodePublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("编码设备公钥: %v", err)
	}

	sign := func(nonce string, body []byte) DeviceAuthInput {
		t.Helper()
		timestamp := e.now.Unix()
		in := DeviceAuthInput{
			Method:      "POST",
			Path:        "/api/v1/auth/register",
			Body:        body,
			Fingerprint: "fp-fresh-nonce",
			Timestamp:   timestamp,
			Nonce:       nonce,
			PublicKey:   encoded,
		}
		canonical := auth.CanonicalString(
			in.Method, in.Path, "", timestamp, nonce, auth.BodyHash(body))
		signature, err := auth.SignForTest(priv, []byte(canonical))
		if err != nil {
			t.Fatalf("签名: %v", err)
		}
		in.Signature = signature
		return in
	}

	body := []byte(`{"username":"fresh-user","password":"password-123"}`)
	result, err := e.svc.Auth.Register(e.ctx(), RegisterInput{
		Username: "fresh-user",
		Password: "password-123",
		Device:   sign("nonce-00000000000000b1", body),
	})
	if err != nil {
		t.Fatalf("首次注册应当成功: %v", err)
	}
	if result.Token == "" || result.Device.Fingerprint != "fp-fresh-nonce" {
		t.Fatalf("注册结果不正确: %+v", result)
	}

	_, err = e.svc.Auth.Register(e.ctx(), RegisterInput{
		Username: "fresh-user",
		Password: "password-123",
		Device:   sign("nonce-00000000000000b1", body),
	})
	requireErrorCode(t, err, "nonce_replayed")
}
