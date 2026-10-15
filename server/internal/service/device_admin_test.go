package service

import (
	"testing"

	"github.com/Sparkzure/PayPrint/server/internal/domain"
)

func TestBlockDeviceKeepsBindingSoItCanBeRestored(t *testing.T) {
	e := newPaymentTestEnv(t)
	userID := e.createUser("dev-owner", domain.RoleUser)
	device := e.createDevice("dev-block", "fp-block", userID)

	if err := e.svc.Devices.BlockDevice(e.ctx(), device.ID, "admin-1", "疑似被盗用", "127.0.0.1"); err != nil {
		t.Fatalf("停用设备失败: %v", err)
	}

	blocked := e.device(device.ID)
	if blocked.Status != string(domain.DeviceRevoked) {
		t.Fatalf("设备应为已停用，实际 %s", blocked.Status)
	}
	if blocked.UserID == nil || *blocked.UserID != userID {
		t.Fatalf("停用必须保留归属（恢复要靠它），实际 %v", blocked.UserID)
	}
	if blocked.PublicKey == "" {
		t.Fatal("停用必须保留公钥固定，否则恢复后设备仍需重新固定密钥")
	}
	if blocked.RevokeReason != "疑似被盗用" {
		t.Fatalf("停用原因应落库，实际 %q", blocked.RevokeReason)
	}

	if _, err := e.svc.Auth.AuthenticateDevice(e.ctx(), DeviceAuthInput{
		Fingerprint: device.Fingerprint,
		Timestamp:   e.now.Unix(),
		Nonce:       "nonce-0123456789abcdef",
		Signature:   "x",
	}, DeviceAuthOptions{AllowNew: true}); err == nil {
		t.Fatal("已停用的设备不应通过任何鉴权")
	}

	if err := e.svc.Devices.AdminRestoreDevice(e.ctx(), device.ID, "admin-1", "误停用", "127.0.0.1"); err != nil {
		t.Fatalf("恢复设备失败: %v", err)
	}

	restored := e.device(device.ID)
	if restored.Status != string(domain.DeviceActive) {
		t.Fatalf("有归属的设备恢复后应为已绑定，实际 %s", restored.Status)
	}
	if restored.UserID == nil || *restored.UserID != userID {
		t.Fatalf("恢复后归属应保持不变，实际 %v", restored.UserID)
	}
	if restored.PublicKey != blocked.PublicKey {
		t.Fatal("恢复不应改动公钥固定")
	}
	if restored.RevokeReason != "" {
		t.Fatalf("恢复后应清掉停用原因，实际 %q", restored.RevokeReason)
	}
}

func TestRestoreLegacyRevokedDeviceGoesUnbound(t *testing.T) {
	e := newPaymentTestEnv(t)
	e.mustExec(`INSERT INTO devices (id, fingerprint, public_key, public_key_alg, status,
		user_id, bound_at, label, platform, app_version, first_seen_at, last_seen_at,
		revoked_at, revoke_reason, created_at, updated_at)
		VALUES ('dev-legacy', 'fp-legacy', '', 'ecdsa-p256', 'revoked', NULL, 0, '', '', '',
			?, ?, ?, '老的停用逻辑会清空归属', ?, ?)`,
		e.now.Unix(), e.now.Unix(), e.now.Unix(), e.now.Unix(), e.now.Unix())

	if err := e.svc.Devices.AdminRestoreDevice(e.ctx(), "dev-legacy", "admin-1", "", "127.0.0.1"); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if got := e.device("dev-legacy").Status; got != string(domain.DeviceUnbound) {
		t.Fatalf("已无归属的停用设备应恢复为已解绑，实际 %s", got)
	}
}

func TestReleasedDeviceIsNoLongerADevice(t *testing.T) {
	e := newPaymentTestEnv(t)
	userID := e.createUser("dev-released", domain.RoleUser)
	device := e.createDevice("dev-released", "fp-released", userID)

	if err := e.svc.Devices.AdminReleaseDevice(e.ctx(), ReleaseDeviceInput{
		DeviceID: device.ID, Reason: "测试：彻底释放", ActorID: "admin-1",
	}); err != nil {
		t.Fatalf("解绑失败: %v", err)
	}

	all, err := e.svc.Devices.ListAll(e.ctx(), 100)
	if err != nil {
		t.Fatalf("读取全站设备失败: %v", err)
	}
	for _, v := range all {
		if v.ID == device.ID {
			t.Fatalf("已释放的行不应出现在管理面板设备列表：%+v", v)
		}
	}

	mine, err := e.svc.Devices.List(e.ctx(), userID, device.ID)
	if err != nil {
		t.Fatalf("读取账号设备失败: %v", err)
	}
	for _, v := range mine {
		if v.ID == device.ID {
			t.Fatalf("已释放的行不应出现在账号设备列表：%+v", v)
		}
	}

	requireErrorCode(t,
		e.svc.Devices.BlockDevice(e.ctx(), device.ID, "admin-1", "", ""), "conflict")
	requireErrorCode(t,
		e.svc.Devices.AdminRestoreDevice(e.ctx(), device.ID, "admin-1", "", ""), "conflict")
	requireErrorCode(t, e.svc.Devices.AdminReleaseDevice(e.ctx(), ReleaseDeviceInput{
		DeviceID: device.ID, Reason: "再来一次", ActorID: "admin-1",
	}), "conflict")
	requireErrorCode(t, e.svc.Devices.AdminReleaseDevice(e.ctx(), ReleaseDeviceInput{
		DeviceID: "dev-not-exist", ActorID: "admin-1",
	}), "device_not_found")
}

func TestBlockAndRestoreDeviceStateGuards(t *testing.T) {
	e := newPaymentTestEnv(t)
	userID := e.createUser("dev-guard", domain.RoleUser)
	device := e.createDevice("dev-guard", "fp-guard", userID)

	requireErrorCode(t,
		e.svc.Devices.AdminRestoreDevice(e.ctx(), device.ID, "admin-1", "", ""), "conflict")

	if err := e.svc.Devices.BlockDevice(e.ctx(), device.ID, "admin-1", "", ""); err != nil {
		t.Fatalf("首次停用应当成功: %v", err)
	}
	requireErrorCode(t,
		e.svc.Devices.BlockDevice(e.ctx(), device.ID, "admin-1", "", ""), "conflict")

	requireErrorCode(t,
		e.svc.Devices.AdminRestoreDevice(e.ctx(), "dev-not-exist", "admin-1", "", ""), "device_not_found")
}
