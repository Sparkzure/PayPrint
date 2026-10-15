package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argonMemoryKiB  = 64 * 1024
	argonIterations = 3
	argonThreads    = 2
	argonSaltLen    = 16
	argonKeyLen     = 32
)

var ErrPasswordMismatch = errors.New("密码不正确")

func HashPassword(plain string) (string, error) {
	if len(plain) < 1 {
		return "", errors.New("密码不能为空")
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("生成密码盐: %w", err)
	}

	key := argon2.IDKey([]byte(plain), salt, argonIterations, argonMemoryKiB, argonThreads, argonKeyLen)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		argonMemoryKiB,
		argonIterations,
		argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

func VerifyPassword(encoded, plain string) error {
	parts := strings.Split(encoded, "$")

	if len(parts) != 6 || parts[1] != "argon2id" {
		return errors.New("密码散列格式不正确")
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return fmt.Errorf("解析密码散列版本: %w", err)
	}
	if version != argon2.Version {
		return fmt.Errorf("不支持的密码散列版本 %d", version)
	}

	var memory, iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return fmt.Errorf("解析密码散列参数: %w", err)
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return fmt.Errorf("解析密码盐: %w", err)
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return fmt.Errorf("解析密码散列值: %w", err)
	}

	got := argon2.IDKey([]byte(plain), salt, iterations, memory, threads, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrPasswordMismatch
	}
	return nil
}
