package service

import (
	"context"
	"crypto/ecdsa"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Sparkzure/PayPrint/server/internal/auth"
	"github.com/Sparkzure/PayPrint/server/internal/domain"
	"github.com/Sparkzure/PayPrint/server/internal/ids"
	"github.com/Sparkzure/PayPrint/server/internal/store"
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{2,31}$`)

const (
	minPasswordLen = 8
	maxPasswordLen = 128
)

type DeviceAuthInput struct {
	Method      string
	Path        string
	RawQuery    string
	Body        []byte
	Fingerprint string
	Timestamp   int64
	Nonce       string
	Signature   string

	PublicKey  string
	Platform   string
	AppVersion string
	Label      string
}

type DeviceAuthOptions struct {
	AllowNew bool

	RequireBound bool
}

type DeviceIdentity struct {
	Device store.Device

	Exists bool

	NeedsKeyPin bool
	Nonce       string
	Timestamp   int64
}

type RegisterInput struct {
	Username string
	Password string
	Device   DeviceAuthInput
}

type CreateUserInput struct {
	Username string
	Password string

	Role    string
	ActorID string
	IP      string
}

func validateCredentials(username, password string) error {
	if !usernamePattern.MatchString(username) {
		return domain.ErrValidation.WithMessage(
			"用户名需为 3-32 位字母、数字、下划线、点或短横线，且以字母或数字开头")
	}
	if n := utf8.RuneCountInString(password); n < minPasswordLen || n > maxPasswordLen {
		return domain.ErrValidation.WithMessage("密码长度需为 8-128 位")
	}
	return nil
}

type AuthResult struct {
	User      store.User
	Device    store.Device
	Token     string
	ExpiresAt int64
}

type SessionIdentity struct {
	User    store.User
	Device  *store.Device
	Session store.Session
}

func (s *SessionIdentity) IsAdmin() bool {
	return s != nil && domain.UserRole(s.User.Role).IsAdmin()
}

func (s *SessionIdentity) DeviceID() string {
	if s == nil || s.Device == nil {
		return ""
	}
	return s.Device.ID
}

type AuthService struct{ *Services }

func (svc *AuthService) AuthenticateDevice(
	ctx context.Context, in DeviceAuthInput, opts DeviceAuthOptions,
) (*DeviceIdentity, error) {
	if strings.TrimSpace(in.Fingerprint) == "" ||
		strings.TrimSpace(in.Nonce) == "" ||
		strings.TrimSpace(in.Signature) == "" ||
		in.Timestamp == 0 {
		return nil, domain.ErrMissingSignature
	}

	skew := int64(svc.Cfg.SignatureSkew.Seconds())
	delta := svc.nowUnix() - in.Timestamp
	if delta < 0 {
		delta = -delta
	}
	if delta > skew {
		return nil, domain.ErrSignatureSkewed
	}

	if len(in.Nonce) < 16 || len(in.Nonce) > 256 {
		return nil, domain.ErrValidation.WithMessage("nonce 长度不合法")
	}

	identity := &DeviceIdentity{Nonce: in.Nonce, Timestamp: in.Timestamp}

	var pubKey *ecdsa.PublicKey
	device, err := svc.Q.GetDeviceByFingerprint(ctx, in.Fingerprint)
	switch {
	case err == nil:

		if device.Status == string(domain.DeviceRevoked) {
			return nil, domain.ErrDeviceRevoked
		}
		identity.Device = device
		identity.Exists = true

		if strings.TrimSpace(device.PublicKey) == "" {

			if strings.TrimSpace(in.PublicKey) == "" {
				return nil, domain.ErrInvalidPublicKey.WithMessage("该设备已解绑，请重新登录以重新登记设备密钥")
			}
			parsed, perr := auth.ParsePublicKey(in.PublicKey)
			if perr != nil {
				return nil, perr
			}
			pubKey = parsed
			identity.NeedsKeyPin = true
			break
		}

		pinned, perr := auth.ParsePublicKey(device.PublicKey)
		if perr != nil {

			return nil, domain.ErrInternal.WithErr(perr)
		}

		if strings.TrimSpace(in.PublicKey) != "" {
			provided, perr := auth.ParsePublicKey(in.PublicKey)
			if perr != nil {
				return nil, perr
			}
			current, _ := auth.EncodePublicKey(pinned)
			candidate, _ := auth.EncodePublicKey(provided)
			if current != candidate {
				return nil, domain.ErrDeviceKeyMismatch
			}
		}
		pubKey = pinned

	case errors.Is(err, sql.ErrNoRows):
		if !opts.AllowNew {
			return nil, domain.ErrDeviceNotFound
		}

		if strings.TrimSpace(in.PublicKey) == "" {
			return nil, domain.ErrInvalidPublicKey.WithMessage("首次登记设备必须同时提交公钥")
		}
		parsed, perr := auth.ParsePublicKey(in.PublicKey)
		if perr != nil {
			return nil, perr
		}
		pubKey = parsed

	default:
		return nil, domain.ErrInternal.WithErr(err)
	}

	if opts.RequireBound {
		if !identity.Exists {
			return nil, domain.ErrDeviceNotFound
		}
		if identity.Device.UserID == nil || *identity.Device.UserID == "" {
			return nil, domain.ErrForbidden.WithMessage("该设备尚未绑定账号")
		}

		owner, err := svc.Q.GetUserByID(ctx, *identity.Device.UserID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, domain.ErrForbidden.WithMessage("该设备绑定的账号不存在")
			}
			return nil, domain.ErrInternal.WithErr(err)
		}
		if owner.Status != string(domain.UserStatusActive) {
			return nil, domain.ErrAccountBanned
		}
	}

	canonical := auth.CanonicalString(in.Method, in.Path, in.RawQuery, in.Timestamp, in.Nonce, auth.BodyHash(in.Body))
	if err := auth.VerifySignature(pubKey, []byte(canonical), in.Signature); err != nil {
		return nil, err
	}

	if err := svc.recordNonce(ctx, svc.Q, in.Fingerprint, in.Nonce); err != nil {
		return nil, err
	}

	if identity.Exists {
		identity.Device = svc.touchDevice(ctx, identity.Device, in)
	}
	return identity, nil
}

func (svc *AuthService) recordNonce(ctx context.Context, q *store.Queries, fingerprint, nonce string) error {
	now := svc.now()
	err := q.CreateNonce(ctx, store.CreateNonceParams{
		Fingerprint: fingerprint,
		Nonce:       nonce,
		CreatedAt:   now.Unix(),
		ExpiresAt:   now.Add(svc.Cfg.NonceTTL).Unix(),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrNonceReplayed
		}
		return domain.ErrInternal.WithErr(err)
	}
	return nil
}

func (svc *AuthService) touchDevice(ctx context.Context, device store.Device, in DeviceAuthInput) store.Device {
	platform := in.Platform
	appVersion := in.AppVersion
	if platform == "" {
		platform = device.Platform
	}
	if appVersion == "" {
		appVersion = device.AppVersion
	}
	now := svc.nowUnix()
	if err := svc.Q.TouchDevice(ctx, store.TouchDeviceParams{
		LastSeenAt: now,
		Platform:   platform,
		AppVersion: appVersion,
		UpdatedAt:  now,
		ID:         device.ID,
	}); err != nil {
		svc.Log.WarnContext(ctx, "刷新设备活跃信息失败", "device_id", device.ID, "error", err)
		return device
	}
	device.LastSeenAt = now
	device.Platform = platform
	device.AppVersion = appVersion
	return device
}

func (svc *AuthService) Register(ctx context.Context, in RegisterInput) (*AuthResult, error) {

	if !svc.Runtime().OpenRegistration {
		return nil, domain.ErrRegistrationClosed
	}

	username := strings.TrimSpace(in.Username)
	if err := validateCredentials(username, in.Password); err != nil {
		return nil, err
	}

	identity, err := svc.AuthenticateDevice(ctx, in.Device, DeviceAuthOptions{AllowNew: true})
	if err != nil {
		return nil, err
	}

	if identity.Exists {
		return nil, domain.ErrDeviceAlreadyUsed
	}

	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return nil, domain.ErrInternal.WithErr(err)
	}

	now := svc.now()
	var result *AuthResult

	err = svc.InTx(ctx, func(q *store.Queries) error {
		userID := ids.New()

		role := "user"
		n, err := q.CountUsers(ctx)
		if err != nil {
			return domain.ErrInternal.WithErr(err)
		}
		if n == 0 {
			role = string(domain.RoleAdmin)
		}

		user, err := q.CreateUser(ctx, store.CreateUserParams{
			ID:           userID,
			Username:     username,
			UsernameCi:   strings.ToLower(username),
			PasswordHash: hash,
			Phone:        "",
			Role:         role,
			CreatedAt:    now.Unix(),
			UpdatedAt:    now.Unix(),
		})
		if err != nil {
			if isUniqueViolation(err) {
				return domain.ErrUsernameTaken
			}
			return domain.ErrInternal.WithErr(err)
		}

		var device store.Device
		var boundDeviceID *string
		if role != string(domain.RoleAdmin) {
			created, err := svc.createDevice(ctx, q, in.Device, &userID, now)
			if err != nil {
				return err
			}
			device = created
			boundDeviceID = &device.ID
		}

		token, expiresAt, err := svc.issueSession(ctx, q, user.ID, boundDeviceID, now)
		if err != nil {
			return err
		}

		svc.Log.InfoContext(ctx, "账号注册", "username", user.Username, "role", role)

		result = &AuthResult{User: user, Device: device, Token: token, ExpiresAt: expiresAt}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (svc *AuthService) AdminCreateUser(ctx context.Context, in CreateUserInput) (*UserView, error) {
	role := domain.UserRole(strings.TrimSpace(in.Role))
	if role == "" {
		role = domain.RoleUser
	}
	switch role {
	case domain.RoleUser, domain.RoleAdmin:
	default:
		return nil, domain.ErrValidation.WithMessage("role 只能是 user 或 admin")
	}

	username := strings.TrimSpace(in.Username)
	if err := validateCredentials(username, in.Password); err != nil {
		return nil, err
	}

	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return nil, domain.ErrInternal.WithErr(err)
	}

	now := svc.now()
	var view UserView
	err = svc.InTx(ctx, func(q *store.Queries) error {
		user, err := q.CreateUser(ctx, store.CreateUserParams{
			ID:           ids.New(),
			Username:     username,
			UsernameCi:   strings.ToLower(username),
			PasswordHash: hash,
			Phone:        "",
			Role:         string(role),
			CreatedAt:    now.Unix(),
			UpdatedAt:    now.Unix(),
		})
		if err != nil {
			if isUniqueViolation(err) {
				return domain.ErrUsernameTaken
			}
			return domain.ErrInternal.WithErr(err)
		}

		view = NewUserView(user)

		svc.Log.InfoContext(ctx, "管理员建号",
			"username", user.Username, "role", role, "actor", in.ActorID, "ip", in.IP)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &view, nil
}

func (svc *AuthService) Login(ctx context.Context, username, password string, in DeviceAuthInput) (*AuthResult, error) {
	identity, err := svc.AuthenticateDevice(ctx, in, DeviceAuthOptions{AllowNew: true})
	if err != nil {
		return nil, err
	}

	user, err := svc.Q.GetUserByUsernameCI(ctx, strings.ToLower(strings.TrimSpace(username)))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {

			return nil, domain.ErrInvalidCredentials
		}
		return nil, domain.ErrInternal.WithErr(err)
	}
	if user.Status != string(domain.UserStatusActive) {
		return nil, domain.ErrAccountBanned
	}
	if err := auth.VerifyPassword(user.PasswordHash, password); err != nil {
		if errors.Is(err, auth.ErrPasswordMismatch) {
			return nil, domain.ErrInvalidCredentials
		}
		return nil, domain.ErrInternal.WithErr(err)
	}

	if domain.UserRole(user.Role).IsAdmin() {
		return nil, domain.ErrAdminUseConsole
	}

	now := svc.now()
	var result *AuthResult

	err = svc.InTx(ctx, func(q *store.Queries) error {
		var device store.Device

		if !identity.Exists {
			created, err := svc.createDevice(ctx, q, in, &user.ID, now)
			if err != nil {
				return err
			}
			device = created
		} else {
			device = identity.Device
			switch {
			case device.UserID == nil || *device.UserID == "":
				pinnedKey, err := resolveBindPublicKey(device, identity, in)
				if err != nil {
					return err
				}
				affected, err := q.BindDevice(ctx, store.BindDeviceParams{
					UserID:     &user.ID,
					BoundAt:    now.Unix(),
					PublicKey:  pinnedKey,
					LastSeenAt: now.Unix(),
					UpdatedAt:  now.Unix(),
					ID:         device.ID,
				})
				if err != nil {
					return domain.ErrInternal.WithErr(err)
				}
				if affected == 0 {

					return domain.ErrDeviceBoundToOther
				}
				device.UserID = &user.ID
				device.BoundAt = now.Unix()
			case *device.UserID != user.ID:
				return domain.ErrDeviceBoundToOther
			}

		}

		token, expiresAt, err := svc.issueSession(ctx, q, user.ID, &device.ID, now)
		if err != nil {
			return err
		}

		result = &AuthResult{User: user, Device: device, Token: token, ExpiresAt: expiresAt}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func resolveBindPublicKey(device store.Device, identity *DeviceIdentity, in DeviceAuthInput) (string, error) {
	if identity.NeedsKeyPin {
		parsed, err := auth.ParsePublicKey(in.PublicKey)
		if err != nil {
			return "", err
		}
		return auth.EncodePublicKey(parsed)
	}
	if strings.TrimSpace(device.PublicKey) == "" {
		return "", domain.ErrInvalidPublicKey.WithMessage("设备公钥缺失，请重新登录")
	}
	return device.PublicKey, nil
}

func (svc *AuthService) createDevice(
	ctx context.Context, q *store.Queries, in DeviceAuthInput, userID *string, now time.Time,
) (store.Device, error) {
	pub, err := auth.ParsePublicKey(in.PublicKey)
	if err != nil {
		return store.Device{}, err
	}
	encoded, err := auth.EncodePublicKey(pub)
	if err != nil {
		return store.Device{}, domain.ErrInternal.WithErr(err)
	}

	label := strings.TrimSpace(in.Label)
	if label == "" {
		label = "我的设备"
	}

	device, err := q.CreateDevice(ctx, store.CreateDeviceParams{
		ID:           ids.New(),
		Fingerprint:  in.Fingerprint,
		PublicKey:    encoded,
		PublicKeyAlg: "ecdsa-p256",
		UserID:       userID,
		BoundAt:      now.Unix(),
		Label:        label,
		Platform:     in.Platform,
		AppVersion:   in.AppVersion,
		FirstSeenAt:  now.Unix(),
		LastSeenAt:   now.Unix(),
		CreatedAt:    now.Unix(),
		UpdatedAt:    now.Unix(),
	})
	if err != nil {

		if isUniqueViolation(err) {
			return store.Device{}, domain.ErrDeviceAlreadyUsed
		}
		return store.Device{}, domain.ErrInternal.WithErr(err)
	}

	return device, nil
}

func (svc *AuthService) issueSession(
	ctx context.Context, q *store.Queries, userID string, deviceID *string, now time.Time,
) (token string, expiresAt int64, err error) {
	token, hash, err := auth.NewSessionToken()
	if err != nil {
		return "", 0, domain.ErrInternal.WithErr(err)
	}
	expiresAt = now.Add(svc.Cfg.SessionTTL).Unix()

	if _, err := q.CreateSession(ctx, store.CreateSessionParams{
		ID:         ids.New(),
		UserID:     userID,
		DeviceID:   deviceID,
		TokenHash:  hash,
		IssuedAt:   now.Unix(),
		ExpiresAt:  expiresAt,
		LastUsedAt: now.Unix(),
	}); err != nil {
		return "", 0, domain.ErrInternal.WithErr(err)
	}
	return token, expiresAt, nil
}

func (svc *AuthService) AuthenticateSession(ctx context.Context, token string) (*SessionIdentity, error) {
	if strings.TrimSpace(token) == "" {
		return nil, domain.ErrUnauthorized
	}

	session, err := svc.Q.GetSessionByTokenHash(ctx, auth.HashToken(token))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrUnauthorized
		}
		return nil, domain.ErrInternal.WithErr(err)
	}
	now := svc.nowUnix()
	if session.RevokedAt != 0 || session.ExpiresAt <= now {
		return nil, domain.ErrUnauthorized
	}

	user, err := svc.Q.GetUserByID(ctx, session.UserID)
	if err != nil {
		return nil, domain.ErrUnauthorized
	}
	if user.Status != string(domain.UserStatusActive) {
		return nil, domain.ErrAccountBanned
	}

	if session.DeviceID == nil && !domain.UserRole(user.Role).IsAdmin() {
		return nil, domain.ErrUnauthorized
	}

	var device *store.Device
	if session.DeviceID != nil {
		d, err := svc.Q.GetDeviceByID(ctx, *session.DeviceID)
		if err != nil {
			return nil, domain.ErrUnauthorized
		}

		if d.Status != string(domain.DeviceActive) ||
			d.UserID == nil || *d.UserID != user.ID {
			return nil, domain.ErrUnauthorized.WithMessage("设备已解绑，请重新登录")
		}
		device = &d
	}

	if err := svc.Q.TouchSession(ctx, store.TouchSessionParams{LastUsedAt: now, ID: session.ID}); err != nil {
		svc.Log.WarnContext(ctx, "刷新会话活跃时间失败", "session_id", session.ID, "error", err)
	}

	return &SessionIdentity{User: user, Device: device, Session: session}, nil
}

type AdminLoginResult struct {
	Admin     UserView `json:"admin"`
	Token     string   `json:"token"`
	ExpiresAt int64    `json:"expires_at"`
}

func (svc *AuthService) AdminLogin(ctx context.Context, username, password, ip string) (*AdminLoginResult, error) {
	user, err := svc.Q.GetUserByUsernameCI(ctx, strings.ToLower(strings.TrimSpace(username)))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {

			return nil, domain.ErrInvalidCredentials
		}
		return nil, domain.ErrInternal.WithErr(err)
	}
	if user.Status != string(domain.UserStatusActive) {
		return nil, domain.ErrAccountBanned
	}
	if err := auth.VerifyPassword(user.PasswordHash, password); err != nil {
		if errors.Is(err, auth.ErrPasswordMismatch) {
			return nil, domain.ErrInvalidCredentials
		}
		return nil, domain.ErrInternal.WithErr(err)
	}

	if !domain.UserRole(user.Role).IsAdmin() {
		return nil, domain.ErrNotAdminAccount
	}

	now := svc.now()
	var result *AdminLoginResult
	err = svc.InTx(ctx, func(q *store.Queries) error {
		token, expiresAt, err := svc.issueSession(ctx, q, user.ID, nil, now)
		if err != nil {
			return err
		}

		result = &AdminLoginResult{
			Admin:     NewUserView(user),
			Token:     token,
			ExpiresAt: expiresAt,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (svc *AuthService) Logout(ctx context.Context, sessionID string) error {
	if err := svc.Q.RevokeSession(ctx, store.RevokeSessionParams{
		RevokedAt: svc.nowUnix(),
		ID:        sessionID,
	}); err != nil {
		return domain.ErrInternal.WithErr(err)
	}
	return nil
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE")
}
