package service

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Sparkzure/PayPrint/server/internal/auth"
	"github.com/Sparkzure/PayPrint/server/internal/config"
	"github.com/Sparkzure/PayPrint/server/internal/database"
	"github.com/Sparkzure/PayPrint/server/internal/domain"
	"github.com/Sparkzure/PayPrint/server/internal/ids"
	"github.com/Sparkzure/PayPrint/server/internal/ipp"
	"github.com/Sparkzure/PayPrint/server/internal/notifier"
	"github.com/Sparkzure/PayPrint/server/internal/store"
)

var testNow = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

const samplePDF = `%PDF-1.4
1 0 obj
<< /Type /Catalog /Pages 2 0 R >>
endobj
2 0 obj
<< /Type /Pages /Kids [3 0 R 4 0 R 5 0 R] /Count 3 >>
endobj
3 0 obj
<< /Type /Page /Parent 2 0 R >>
endobj
4 0 obj
<< /Type /Page /Parent 2 0 R >>
endobj
5 0 obj
<< /Type /Page /Parent 2 0 R >>
endobj
trailer
<< /Root 1 0 R >>
%%EOF`

type env struct {
	t   *testing.T
	svc *Services
	now *time.Time
}

func newEnv(t *testing.T, mutate func(*config.Config)) *env {
	t.Helper()

	dir := t.TempDir()
	db, err := database.Open(filepath.Join(dir, "test.db"), database.Options{})
	if err != nil {
		t.Fatalf("打开测试数据库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatalf("执行迁移: %v", err)
	}

	cfg := &config.Config{
		Env:            "test",
		DBPath:         filepath.Join(dir, "test.db"),
		StorageDir:     filepath.Join(dir, "files"),
		LogLevel:       "error",
		SessionTTL:     time.Hour,
		SignatureSkew:  5 * time.Minute,
		NonceTTL:       15 * time.Minute,
		MaxUploadBytes: 5 << 20,

		IPP: config.IPPConfig{
			UserName:  "test",
			JobFormat: "application/pdf",
			RetryBase: time.Second,
			RetryMax:  time.Minute,
		},
		Worker:             config.WorkerConfig{Enabled: false, Interval: time.Second, Batch: 10, Lease: time.Minute},
		WeChat:             config.WeChatConfig{Timeout: time.Second, MaxAttemp: 3},
		DefaultPrinterURI:  "ipp://127.0.0.1:631/ipp/print",
		DefaultPrinterName: "测试打印机",
	}
	if mutate != nil {
		mutate(cfg)
	}

	triggers := notifier.NewRegistry()
	triggers.Register(notifier.WeChatAndroidTrigger{})

	clock := testNow
	svc := New(&Deps{
		DB:       db,
		Q:        store.New(db),
		Cfg:      cfg,
		IPP:      ipp.NewClient(time.Second, "test"),
		Triggers: triggers,
		Queriers: notifier.NewQueriers(nil),
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:      func() time.Time { return clock },

		Prober: stubProber{},
	})

	e := &env{t: t, svc: svc, now: &clock}
	if err := svc.Printers.Bootstrap(context.Background()); err != nil {
		t.Fatalf("登记默认打印机: %v", err)
	}

	seedBootstrapAdmin(t, svc)
	return e
}

func seedBootstrapAdmin(t *testing.T, svc *Services) {
	t.Helper()
	hash, err := auth.HashPassword("bootstrap-admin-password")
	if err != nil {
		t.Fatalf("散列密码: %v", err)
	}
	if _, err := svc.Q.CreateUser(context.Background(), store.CreateUserParams{
		ID:           ids.New(),
		Username:     "bootstrap-admin",
		UsernameCi:   "bootstrap-admin",
		PasswordHash: hash,
		Role:         string(domain.RoleAdmin),
		CreatedAt:    testNow.Unix(),
		UpdatedAt:    testNow.Unix(),
	}); err != nil {
		t.Fatalf("预置管理员: %v", err)
	}
}

func (e *env) advance(d time.Duration) { *e.now = e.now.Add(d) }

type deviceFixture struct {
	priv *ecdsa.PrivateKey
	pub  string
	fp   string
}

func newDeviceFixture(t *testing.T, fingerprint string) deviceFixture {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成设备密钥: %v", err)
	}
	pub, err := auth.EncodePublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("编码设备公钥: %v", err)
	}
	return deviceFixture{priv: priv, pub: pub, fp: fingerprint}
}

func (d deviceFixture) signed(t *testing.T, method, path string, body []byte, at time.Time, nonce string) DeviceAuthInput {
	t.Helper()
	canonical := auth.CanonicalString(method, path, "", at.Unix(), nonce, auth.BodyHash(body))
	sig, err := auth.SignForTest(d.priv, []byte(canonical))
	if err != nil {
		t.Fatalf("生成设备签名: %v", err)
	}
	return DeviceAuthInput{
		Method:      method,
		Path:        path,
		Body:        body,
		Fingerprint: d.fp,
		Timestamp:   at.Unix(),
		Nonce:       nonce,
		Signature:   sig,
		PublicKey:   d.pub,
		Platform:    "android",
		AppVersion:  "1.0.0",
		Label:       "测试机",
	}
}

func newNonce(t *testing.T) string {
	t.Helper()
	nonce, err := ids.NewRandom(24)
	if err != nil {
		t.Fatalf("生成 nonce: %v", err)
	}
	return nonce
}

func assertCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("期望错误码 %s，实际没有错误", code)
	}
	appErr, ok := domain.AsAppError(err)
	if !ok {
		t.Fatalf("期望应用错误 %s，实际 %v", code, err)
	}
	if appErr.Code != code {
		t.Fatalf("错误码 = %s（%s），期望 %s", appErr.Code, appErr.Message, code)
	}
}

func register(t *testing.T, e *env, d deviceFixture, username, password string) *AuthResult {
	t.Helper()
	result, err := e.svc.Auth.Register(context.Background(), RegisterInput{
		Username: username,
		Password: password,
		Device:   d.signed(t, "POST", "/api/v1/auth/register", nil, *e.now, newNonce(t)),
	})
	if err != nil {
		t.Fatalf("注册 %s 失败: %v", username, err)
	}
	return result
}

func TestRegisterBindsDeviceAndRefusesTheUsedHardware(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()

	d := newDeviceFixture(t, "fp-alice")
	result := register(t, e, d, "alice", "password123")

	if result.Device.UserID == nil || *result.Device.UserID != result.User.ID {
		t.Fatal("注册后设备应绑定到该账号")
	}
	if result.Device.Status != string(domain.DeviceActive) {
		t.Fatalf("设备状态 = %s，期望 active", result.Device.Status)
	}
	if result.Token == "" {
		t.Fatal("注册应返回会话令牌")
	}

	_, err := e.svc.Auth.Register(ctx, RegisterInput{
		Username: "bob",
		Password: "password123",
		Device:   d.signed(t, "POST", "/api/v1/auth/register", nil, *e.now, newNonce(t)),
	})
	assertCode(t, err, "device_already_used")

	_, err = e.svc.Auth.Register(ctx, RegisterInput{
		Username: "alice2",
		Password: "password123",
		Device:   d.signed(t, "POST", "/api/v1/auth/register", nil, *e.now, newNonce(t)),
	})
	assertCode(t, err, "device_already_used")

	imposter := newDeviceFixture(t, "fp-alice")
	_, err = e.svc.Auth.Register(ctx, RegisterInput{
		Username: "carol",
		Password: "password123",
		Device:   imposter.signed(t, "POST", "/api/v1/auth/register", nil, *e.now, newNonce(t)),
	})
	assertCode(t, err, "device_key_mismatch")
}

func TestRegisterRejectsDuplicateUsername(t *testing.T) {
	e := newEnv(t, nil)
	register(t, e, newDeviceFixture(t, "fp-a"), "alice", "password123")

	d2 := newDeviceFixture(t, "fp-b")
	_, err := e.svc.Auth.Register(context.Background(), RegisterInput{
		Username: "alice",
		Password: "password123",
		Device:   d2.signed(t, "POST", "/api/v1/auth/register", nil, *e.now, newNonce(t)),
	})
	assertCode(t, err, "username_taken")
}

func TestRegisterValidatesInput(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()

	cases := []struct{ name, username, password string }{
		{"用户名过短", "ab", "password123"},
		{"用户名含非法字符", "al ice!", "password123"},
		{"密码过短", "alice", "short"},
	}
	for i, tc := range cases {
		d := newDeviceFixture(t, "fp-validate-"+tc.name)
		_, err := e.svc.Auth.Register(ctx, RegisterInput{
			Username: tc.username,
			Password: tc.password,
			Device:   d.signed(t, "POST", "/api/v1/auth/register", nil, *e.now, newNonce(t)),
		})
		if err == nil {
			t.Fatalf("用例 %d (%s) 应当被拒绝", i, tc.name)
		}
	}
}

func TestRegisterClosedByConfig(t *testing.T) {
	e := newEnv(t, nil)
	if _, err := e.svc.Settings.Update(context.Background(), UpdateSettingsInput{
		Set: map[string]string{SettingOpenRegistration: "false"},
	}); err != nil {
		t.Fatalf("关闭自助注册: %v", err)
	}
	d := newDeviceFixture(t, "fp-closed")
	_, err := e.svc.Auth.Register(context.Background(), RegisterInput{
		Username: "alice",
		Password: "password123",
		Device:   d.signed(t, "POST", "/api/v1/auth/register", nil, *e.now, newNonce(t)),
	})
	assertCode(t, err, "registration_closed")
}

func TestFirstRegisteredAccountBecomesAdmin(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	if _, err := e.svc.DB.ExecContext(ctx, `DELETE FROM users`); err != nil {
		t.Fatalf("清空账号模拟空库: %v", err)
	}

	d1 := newDeviceFixture(t, "fp-root")
	root := register(t, e, d1, "root", "password123")
	if root.User.Role != string(domain.RoleAdmin) {
		t.Fatalf("空库的首个注册账号应当是管理员，实际 %q", root.User.Role)
	}

	if root.Device.ID != "" {
		t.Fatalf("管理员注册不应带有设备，实际 %q", root.Device.ID)
	}

	onRoot := register(t, e, d1, "member-on-root", "password123")
	if onRoot.Device.ID == "" {
		t.Fatal("成员账号注册后应当绑定该设备")
	}
	if onRoot.Device.Fingerprint != "fp-root" {
		t.Fatalf("绑定的指纹 = %q，期望 fp-root", onRoot.Device.Fingerprint)
	}
	if onRoot.Device.UserID == nil || *onRoot.Device.UserID != onRoot.User.ID {
		t.Fatal("成员设备应绑定到该账号")
	}

	session, err := e.svc.Auth.AuthenticateSession(ctx, root.Token)
	if err != nil {
		t.Fatalf("管理员会话应当有效: %v", err)
	}
	if !session.IsAdmin() {
		t.Fatal("bootstrap 账号的会话应当是管理员会话")
	}
	if session.Device != nil {
		t.Fatalf("管理员会话不应绑定设备，实际 %q", session.Device.ID)
	}

	d2 := newDeviceFixture(t, "fp-second")
	second := register(t, e, d2, "second", "password123")
	if second.User.Role != string(domain.RoleUser) {
		t.Fatalf("第二个注册账号应当是普通用户，实际 %q", second.User.Role)
	}
}

func TestLoginOnSecondDeviceAndOwnershipConflict(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()

	d1 := newDeviceFixture(t, "fp-alice")
	alice := register(t, e, d1, "alice", "password123")
	register(t, e, newDeviceFixture(t, "fp-bob"), "bob", "password123")

	d2 := newDeviceFixture(t, "fp-alice-2")
	result, err := e.svc.Auth.Login(ctx, "alice", "password123",
		d2.signed(t, "POST", "/api/v1/auth/login", nil, *e.now, newNonce(t)))
	if err != nil {
		t.Fatalf("第二台设备登录应当成功: %v", err)
	}
	if result.User.ID != alice.User.ID {
		t.Fatal("登录返回的账号不正确")
	}
	if result.Device.UserID == nil || *result.Device.UserID != alice.User.ID {
		t.Fatal("新设备应绑定到登录账号")
	}

	_, err = e.svc.Auth.Login(ctx, "bob", "password123",
		d1.signed(t, "POST", "/api/v1/auth/login", nil, *e.now, newNonce(t)))
	assertCode(t, err, "device_bound_to_other_account")
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	register(t, e, newDeviceFixture(t, "fp-pw"), "alice", "password123")

	_, err := e.svc.Auth.Login(ctx, "alice", "wrong-password",
		newDeviceFixture(t, "fp-pw-2").signed(t, "POST", "/api/v1/auth/login", nil, *e.now, newNonce(t)))
	assertCode(t, err, "invalid_credentials")

	_, err = e.svc.Auth.Login(ctx, "nobody", "password123",
		newDeviceFixture(t, "fp-pw-3").signed(t, "POST", "/api/v1/auth/login", nil, *e.now, newNonce(t)))
	assertCode(t, err, "invalid_credentials")
}

func TestDeviceAuthRejectsReplaySkewAndBadSignature(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	d := newDeviceFixture(t, "fp-sec")
	register(t, e, d, "alice", "password123")

	nonce := newNonce(t)
	login := func(in DeviceAuthInput) error {
		_, err := e.svc.Auth.Login(ctx, "alice", "password123", in)
		return err
	}

	if err := login(d.signed(t, "POST", "/api/v1/auth/login", nil, *e.now, nonce)); err != nil {
		t.Fatalf("首次请求应当成功: %v", err)
	}

	assertCode(t, login(d.signed(t, "POST", "/api/v1/auth/login", nil, *e.now, nonce)), "nonce_replayed")

	stale := *e.now
	e.advance(time.Hour)
	assertCode(t, login(d.signed(t, "POST", "/api/v1/auth/login", nil, stale, newNonce(t))),
		"signature_expired")

	in := d.signed(t, "POST", "/api/v1/auth/login", []byte(`{"a":1}`), *e.now, newNonce(t))
	in.Body = []byte(`{"a":2}`)
	assertCode(t, login(in), "invalid_signature")

	in = d.signed(t, "POST", "/api/v1/auth/login", nil, *e.now, newNonce(t))
	in.Signature = ""
	assertCode(t, login(in), "missing_signature")

	imposter := newDeviceFixture(t, "fp-sec")
	in = imposter.signed(t, "POST", "/api/v1/auth/login", nil, *e.now, newNonce(t))
	assertCode(t, login(in), "device_key_mismatch")
}

func TestUnknownDeviceCannotAccessBoundEndpoints(t *testing.T) {
	e := newEnv(t, nil)
	_, err := e.svc.Auth.AuthenticateDevice(context.Background(),
		newDeviceFixture(t, "fp-unknown").signed(t, "GET", "/api/v1/me", nil, *e.now, newNonce(t)),
		DeviceAuthOptions{RequireBound: true})
	assertCode(t, err, "device_not_found")
}

func uploadFile(t *testing.T, e *env, userID string) *PrintFileView {
	t.Helper()
	view, err := e.svc.Files.Upload(context.Background(), UploadInput{
		UserID:      userID,
		Filename:    "报告.pdf",
		ContentType: "application/pdf",
		Reader:      strings.NewReader(samplePDF),
	})
	if err != nil {
		t.Fatalf("上传文件失败: %v", err)
	}
	if view.PageCount != 3 {
		t.Fatalf("识别页数 = %d，期望 3", view.PageCount)
	}
	return view
}

func createOrder(t *testing.T, e *env, account *AuthResult, printerID, fileID string, params domain.PrintParams) *OrderView {
	t.Helper()
	view, err := e.svc.Orders.Create(context.Background(), CreateOrderInput{
		UserID:            account.User.ID,
		DeviceLabel:       account.Device.Label,
		DeviceFingerprint: account.Device.Fingerprint,
		FileID:            fileID,
		PrinterID:         printerID,
		Params:            params,
	})
	if err != nil {
		t.Fatalf("下单失败: %v", err)
	}
	return view
}

func createOrderErr(t *testing.T, e *env, account *AuthResult, printerID, fileID string, params domain.PrintParams) (*OrderView, error) {
	t.Helper()
	return e.svc.Orders.Create(context.Background(), CreateOrderInput{
		UserID:            account.User.ID,
		DeviceLabel:       account.Device.Label,
		DeviceFingerprint: account.Device.Fingerprint,
		FileID:            fileID,
		PrinterID:         printerID,
		Params:            params,
	})
}

func TestCreateOrderPricesOnServerSide(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()

	account := register(t, e, newDeviceFixture(t, "fp-price"), "carol", "password123")
	printer, _ := e.svc.Printers.Upsert(ctx, UpsertPrinterInput{
		Code: "T01", Name: "测试机", CupsURI: "ipp://127.0.0.1:631/ipp/print",
		Status: "online", PriceSingleBWFen: 100, PriceSingleColorFen: 300,
		SupportsColor: true, SupportsDuplex: true, PaperSizes: []string{"A4"}, MaxCopies: 10,
	})
	file := uploadFile(t, e, account.User.ID)

	view := createOrder(t, e, account, printer.ID, file.ID, domain.PrintParams{Copies: 2})
	if view.AmountFen != 600 {
		t.Fatalf("金额 = %d 分，期望 600 分", view.AmountFen)
	}
	if view.Status != string(domain.OrderAwaitingPayment) {
		t.Fatalf("状态 = %s，期望 awaiting_payment", view.Status)
	}
	if view.Cost == nil || view.Params == nil {
		t.Fatal("订单应固化参数与计价快照")
	}
	if view.ExpiresAt <= e.now.Unix() {
		t.Fatal("订单应带支付截止时间")
	}
	if !view.CanPay || !view.CanCancel {
		t.Fatal("待支付订单应可支付且可取消")
	}
}

func TestCreateOrderRejectsUnsupportedCapability(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()

	account := register(t, e, newDeviceFixture(t, "fp-cap"), "carol", "password123")
	printer, _ := e.svc.Printers.Upsert(ctx, UpsertPrinterInput{
		Code: "T02", Name: "黑白机", CupsURI: "ipp://127.0.0.1:631/ipp/print",
		Status: "online", PriceSingleBWFen: 100,
		SupportsColor: false, SupportsDuplex: false, PaperSizes: []string{"A4"}, MaxCopies: 1,
	})
	file := uploadFile(t, e, account.User.ID)

	_, err := e.svc.Orders.Create(ctx, CreateOrderInput{
		UserID: account.User.ID,
		FileID: file.ID, PrinterID: printer.ID,
		Params: domain.PrintParams{ColorMode: domain.ColorModeColor},
	})
	assertCode(t, err, "param_unsupported")

	_, err = e.svc.Orders.Create(ctx, CreateOrderInput{
		UserID: account.User.ID,
		FileID: file.ID, PrinterID: printer.ID,
		Params: domain.PrintParams{Duplex: domain.DuplexLongEdge},
	})
	assertCode(t, err, "param_unsupported")

	_, err = e.svc.Orders.Create(ctx, CreateOrderInput{
		UserID: account.User.ID,
		FileID: file.ID, PrinterID: printer.ID,
		Params: domain.PrintParams{Copies: 5},
	})
	assertCode(t, err, "param_unsupported")

	other := register(t, e, newDeviceFixture(t, "fp-other"), "dave", "password123")
	_, err = e.svc.Orders.Create(ctx, CreateOrderInput{
		UserID: other.User.ID,
		FileID: file.ID, PrinterID: printer.ID,
		Params: domain.PrintParams{},
	})
	assertCode(t, err, "file_not_found")
}

func TestPaymentFlowRequiresManualRelease(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()

	d := newDeviceFixture(t, "fp-flow")
	account := register(t, e, d, "carol", "password123")
	printer, _ := e.svc.Printers.Upsert(ctx, UpsertPrinterInput{
		Code: "T03", Name: "测试机", CupsURI: "ipp://127.0.0.1:631/ipp/print",
		Status: "online", PriceSingleBWFen: 100, PriceSingleColorFen: 300,
		SupportsColor: true, SupportsDuplex: true, PaperSizes: []string{"A4"}, MaxCopies: 10,
	})
	file := uploadFile(t, e, account.User.ID)
	order := createOrder(t, e, account, printer.ID, file.ID, domain.DefaultPrintParams())

	_, err := reportPayment(t, e, account.User.ID, order.OrderNo, "evt-wrong", order.AmountFen-1)
	assertCode(t, err, "amount_mismatch")

	detail, err := e.svc.Orders.Detail(ctx, account.User.ID, order.ID)
	if err != nil {
		t.Fatalf("查询订单失败: %v", err)
	}
	if detail.Status != string(domain.OrderAwaitingPayment) {
		t.Fatalf("金额不符后订单状态 = %s，期望仍为 awaiting_payment", detail.Status)
	}

	result, err := reportPayment(t, e, account.User.ID, order.OrderNo, "evt-ok", order.AmountFen)
	if err != nil {
		t.Fatalf("合法上报应当被接受: %v", err)
	}
	if !result.Accepted || result.Duplicate {
		t.Fatalf("首次上报应被接受且非重复: %+v", result)
	}
	if result.JobQueued {
		t.Fatal("默认部署下，支付上报不应直接产生打印任务")
	}
	if !result.CanRelease {
		t.Fatal("支付确认后应提示客户端可以确认打印")
	}

	detail, _ = e.svc.Orders.Detail(ctx, account.User.ID, order.ID)
	if detail.Status != string(domain.OrderAwaitingRelease) {
		t.Fatalf("订单状态 = %s，期望 awaiting_release", detail.Status)
	}
	if !detail.CanRelease {
		t.Fatal("待确认打印的订单 CanRelease 应为 true")
	}

	if _, err := e.svc.Q.GetJobByOrderID(ctx, order.ID); err == nil {
		t.Fatal("支付确认后不应存在打印任务（打印必须由用户手动放行）")
	}
	due, err := e.svc.Q.ListDueJobs(ctx, store.ListDueJobsParams{NextRetryAt: e.now.Unix(), Limit: 10})
	if err != nil {
		t.Fatalf("查询待投递任务失败: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("用户确认前不应有可投递任务，实际 %d 条", len(due))
	}

	spent, entries, err := e.svc.Payments.LedgerSnapshot(ctx, account.User.ID, 10)
	if err != nil {
		t.Fatalf("查询流水失败: %v", err)
	}
	if spent != order.AmountFen {
		t.Fatalf("累计支出 = %d 分，期望 %d 分", spent, order.AmountFen)
	}
	if len(entries) != 1 || entries[0].EntryType != string(domain.LedgerPayment) {
		t.Fatalf("应有且仅有一条支付流水，实际 %+v", entries)
	}

	dup, err := reportPayment(t, e, account.User.ID, order.OrderNo, "evt-ok", order.AmountFen)
	if err != nil {
		t.Fatalf("重复上报应当幂等成功: %v", err)
	}
	if !dup.Duplicate {
		t.Fatal("重复上报应标记为 duplicate")
	}

	released, err := releaseOrder(t, e, account.User.ID, order.ID)
	if err != nil {
		t.Fatalf("确认打印失败: %v", err)
	}
	if released.Status != string(domain.OrderQueued) {
		t.Fatalf("确认后订单状态 = %s，期望 queued", released.Status)
	}
	if released.JobStatus != string(domain.JobPending) {
		t.Fatalf("确认后任务状态 = %s，期望 pending", released.JobStatus)
	}

	again, err := releaseOrder(t, e, account.User.ID, order.ID)
	if err != nil {
		t.Fatalf("重复确认应当幂等成功: %v", err)
	}
	if again.Status != string(domain.OrderQueued) {
		t.Fatalf("重复确认后状态 = %s，期望仍为 queued", again.Status)
	}
	jobs, err := e.svc.Q.ListDueJobs(ctx, store.ListDueJobsParams{NextRetryAt: e.now.Unix(), Limit: 10})
	if err != nil {
		t.Fatalf("查询待投递任务失败: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("一个订单只能有一个打印任务，实际 %d 条", len(jobs))
	}

	dup2, err := reportPayment(t, e, account.User.ID, order.OrderNo, "evt-ok-2", order.AmountFen)
	if err != nil {
		t.Fatalf("已放行订单的再次上报应当幂等成功: %v", err)
	}
	if !dup2.Duplicate {
		t.Fatal("已放行订单的再次上报应标记为 duplicate")
	}

	_, entries, _ = e.svc.Payments.LedgerSnapshot(ctx, account.User.ID, 10)
	if len(entries) != 1 {
		t.Fatalf("重复上报不应产生额外流水，实际 %d 条", len(entries))
	}
}

func TestPaymentReportRejectsOtherAccountsOrder(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()

	owner := register(t, e, newDeviceFixture(t, "fp-owner"), "carol", "password123")
	printer, _ := e.svc.Printers.Upsert(ctx, UpsertPrinterInput{
		Code: "T04", Name: "测试机", CupsURI: "ipp://127.0.0.1:631/ipp/print",
		Status: "online", PriceSingleBWFen: 100, PaperSizes: []string{"A4"}, MaxCopies: 10,
	})
	file := uploadFile(t, e, owner.User.ID)
	order := createOrder(t, e, owner, printer.ID, file.ID, domain.DefaultPrintParams())

	thief := register(t, e, newDeviceFixture(t, "fp-thief"), "dave", "password123")
	_, err := e.svc.Payments.Report(ctx, ReportInput{
		Report: &notifier.Report{
			EventID:   "evt-thief",
			Source:    notifier.SourceWeChatAndroid,
			Channel:   domain.ChannelWeChat,
			OrderNo:   order.OrderNo,
			AmountFen: order.AmountFen,
			PaidAt:    e.now.Unix(),
		},
		UserID: thief.User.ID,
	})
	assertCode(t, err, "order_not_owned")

	detail, err := e.svc.Orders.Detail(ctx, owner.User.ID, order.ID)
	if err != nil {
		t.Fatalf("查询订单失败: %v", err)
	}
	if detail.Status != string(domain.OrderAwaitingPayment) {
		t.Fatalf("越权上报不应改动订单，实际状态 %s", detail.Status)
	}
}

func TestPaymentReportRejectsExpiredOrder(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()

	d := newDeviceFixture(t, "fp-expire")
	account := register(t, e, d, "carol", "password123")
	printer, _ := e.svc.Printers.Upsert(ctx, UpsertPrinterInput{
		Code: "T05", Name: "测试机", CupsURI: "ipp://127.0.0.1:631/ipp/print",
		Status: "online", PriceSingleBWFen: 100, PaperSizes: []string{"A4"}, MaxCopies: 10,
	})
	file := uploadFile(t, e, account.User.ID)
	order := createOrder(t, e, account, printer.ID, file.ID, domain.DefaultPrintParams())

	e.advance(20 * time.Minute)

	_, err := e.svc.Payments.Report(ctx, ReportInput{
		Report: &notifier.Report{
			EventID:   "evt-late",
			Source:    notifier.SourceWeChatAndroid,
			Channel:   domain.ChannelWeChat,
			OrderNo:   order.OrderNo,
			AmountFen: order.AmountFen,
			PaidAt:    e.now.Unix(),
		},
		UserID: account.User.ID,
	})
	assertCode(t, err, "order_expired")

	detail, _ := e.svc.Orders.Detail(ctx, account.User.ID, order.ID)
	if detail.Status != string(domain.OrderExpired) {
		t.Fatalf("超时订单状态 = %s，期望 expired", detail.Status)
	}
}

func TestExpireStaleOrders(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()

	account := register(t, e, newDeviceFixture(t, "fp-sweep"), "carol", "password123")
	printer, _ := e.svc.Printers.Upsert(ctx, UpsertPrinterInput{
		Code: "T06", Name: "测试机", CupsURI: "ipp://127.0.0.1:631/ipp/print",
		Status: "online", PriceSingleBWFen: 100, PaperSizes: []string{"A4"}, MaxCopies: 10,
	})
	file := uploadFile(t, e, account.User.ID)
	createOrder(t, e, account, printer.ID, file.ID, domain.DefaultPrintParams())

	e.advance(30 * time.Minute)
	closed, err := e.svc.Orders.ExpireStaleOrders(ctx, 100)
	if err != nil {
		t.Fatalf("关闭超时订单失败: %v", err)
	}
	if closed != 1 {
		t.Fatalf("关闭数量 = %d，期望 1", closed)
	}

	views, total, err := e.svc.Orders.List(ctx, account.User.ID, 10, 0)
	if err != nil {
		t.Fatalf("查询订单列表失败: %v", err)
	}
	if total != 1 || views[0].Status != string(domain.OrderExpired) {
		t.Fatalf("订单列表状态不正确: %+v", views)
	}
}

type stubQuerier struct {
	state  notifier.QueryState
	amount int64
}

func (s stubQuerier) Name() string { return "stub" }

func (s stubQuerier) Query(context.Context, string, string) (*notifier.QueryResult, error) {
	return &notifier.QueryResult{State: s.state, AmountFen: s.amount, TradeState: string(s.state)}, nil
}

func TestPaymentVerificationConfirmKeepsOrderAwaitingRelease(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()

	d := newDeviceFixture(t, "fp-verify")
	account := register(t, e, d, "carol", "password123")
	printer, _ := e.svc.Printers.Upsert(ctx, UpsertPrinterInput{
		Code: "T07", Name: "测试机", CupsURI: "ipp://127.0.0.1:631/ipp/print",
		Status: "online", PriceSingleBWFen: 100, PaperSizes: []string{"A4"}, MaxCopies: 10,
	})
	file := uploadFile(t, e, account.User.ID)
	order := createOrder(t, e, account, printer.ID, file.ID, domain.DefaultPrintParams())

	e.svc.Queriers.Register(string(domain.ChannelWeChat), stubQuerier{state: notifier.QueryPaid, amount: order.AmountFen})

	if _, err := e.svc.Payments.Report(ctx, ReportInput{
		Report: &notifier.Report{
			EventID: "evt-verify", Source: notifier.SourceWeChatAndroid,
			Channel: domain.ChannelWeChat, OrderNo: order.OrderNo, AmountFen: order.AmountFen,
		},
		UserID: account.User.ID,
	}); err != nil {
		t.Fatalf("上报失败: %v", err)
	}

	processed, err := e.svc.Jobs.RunVerifications(ctx, 10)
	if err != nil {
		t.Fatalf("兜底核验失败: %v", err)
	}
	if processed != 1 {
		t.Fatalf("处理数量 = %d，期望 1", processed)
	}

	detail, _ := e.svc.Orders.Detail(ctx, account.User.ID, order.ID)
	if detail.Status != string(domain.OrderAwaitingRelease) {
		t.Fatalf("核验确认后订单状态 = %s，期望 awaiting_release", detail.Status)
	}

	if !detail.CanRelease {
		t.Fatal("兜底核验通过后，订单应仍然可以由用户确认打印")
	}

	if _, err := e.svc.Q.GetJobByOrderID(ctx, order.ID); err == nil {
		t.Fatal("核验通过不应创建打印任务")
	}
}

func TestMaxPendingOrdersPerUser(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	if _, err := e.svc.Settings.Update(ctx, UpdateSettingsInput{
		Set: map[string]string{SettingMaxPendingOrders: "1"},
	}); err != nil {
		t.Fatalf("设置待支付上限: %v", err)
	}

	d := newDeviceFixture(t, "fp-cap")
	account := register(t, e, d, "dave", "password123")
	printer, _ := e.svc.Printers.Upsert(ctx, UpsertPrinterInput{
		Code: "C01", Name: "限额测试机", CupsURI: "ipp://127.0.0.1:631/ipp/print",
		Status: "online", PriceSingleBWFen: 100, PaperSizes: []string{"A4"}, MaxCopies: 10,
	})
	file := uploadFile(t, e, account.User.ID)

	first := createOrder(t, e, account, printer.ID, file.ID, domain.DefaultPrintParams())
	_, err := createOrderErr(t, e, account, printer.ID, file.ID, domain.DefaultPrintParams())
	assertCode(t, err, "too_many_pending_orders")

	if _, err := reportPayment(t, e, account.User.ID, first.OrderNo, "evt-cap", first.AmountFen); err != nil {
		t.Fatalf("支付上报失败: %v", err)
	}
	third := createOrder(t, e, account, printer.ID, file.ID, domain.DefaultPrintParams())
	if third.Status != string(domain.OrderAwaitingPayment) {
		t.Fatalf("名额释放后应能继续下单，实际 %s", third.Status)
	}
}

func TestRequirePaymentVerificationGatesRelease(t *testing.T) {
	e := newEnv(t, nil)
	if _, err := e.svc.Settings.Update(context.Background(), UpdateSettingsInput{
		Set: map[string]string{SettingPaymentVerify: "true"},
	}); err != nil {
		t.Fatalf("打开强制核验: %v", err)
	}
	ctx := context.Background()

	d := newDeviceFixture(t, "fp-gate")
	account := register(t, e, d, "carol", "password123")
	printer, _ := e.svc.Printers.Upsert(ctx, UpsertPrinterInput{
		Code: "T08", Name: "测试机", CupsURI: "ipp://127.0.0.1:631/ipp/print",
		Status: "online", PriceSingleBWFen: 100, PaperSizes: []string{"A4"}, MaxCopies: 10,
	})
	file := uploadFile(t, e, account.User.ID)
	order := createOrder(t, e, account, printer.ID, file.ID, domain.DefaultPrintParams())

	if _, err := reportPayment(t, e, account.User.ID, order.OrderNo, "evt-gate", order.AmountFen); err != nil {
		t.Fatalf("上报失败: %v", err)
	}
	_, err := releaseOrder(t, e, account.User.ID, order.ID)
	assertCode(t, err, "payment_unverified")

	e.svc.Queriers.Register(string(domain.ChannelWeChat),
		stubQuerier{state: notifier.QueryPaid, amount: order.AmountFen})
	released, err := releaseOrder(t, e, account.User.ID, order.ID)
	if err != nil {
		t.Fatalf("官方确认后应当可以放行: %v", err)
	}
	if released.Status != string(domain.OrderQueued) {
		t.Fatalf("订单状态 = %s，期望 queued", released.Status)
	}
	if released.JobStatus != string(domain.JobPending) {
		t.Fatalf("任务状态 = %s，期望 pending", released.JobStatus)
	}
}

func TestVerificationMismatchFlagsOrderAsDisputed(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()

	d := newDeviceFixture(t, "fp-dispute")
	account := register(t, e, d, "carol", "password123")
	printer, _ := e.svc.Printers.Upsert(ctx, UpsertPrinterInput{
		Code: "T09", Name: "测试机", CupsURI: "ipp://127.0.0.1:631/ipp/print",
		Status: "online", PriceSingleBWFen: 100, PaperSizes: []string{"A4"}, MaxCopies: 10,
	})
	file := uploadFile(t, e, account.User.ID)
	order := createOrder(t, e, account, printer.ID, file.ID, domain.DefaultPrintParams())

	e.svc.Queriers.Register(string(domain.ChannelWeChat), stubQuerier{state: notifier.QueryUnpaid})

	if _, err := reportPayment(t, e, account.User.ID, order.OrderNo, "evt-dispute", order.AmountFen); err != nil {
		t.Fatalf("上报失败: %v", err)
	}

	if _, err := e.svc.Jobs.RunVerifications(ctx, 10); err != nil {
		t.Fatalf("兜底核验失败: %v", err)
	}

	detail, _ := e.svc.Orders.Detail(ctx, account.User.ID, order.ID)
	if detail.Status != string(domain.OrderDisputed) {
		t.Fatalf("订单状态 = %s，期望 disputed", detail.Status)
	}
	if detail.CanRelease {
		t.Fatal("异常待核的订单不应允许确认打印")
	}

	_, err := releaseOrder(t, e, account.User.ID, order.ID)
	assertCode(t, err, "order_not_releasable")

	due, _ := e.svc.Q.ListDueJobs(ctx, store.ListDueJobsParams{NextRetryAt: e.now.Unix(), Limit: 10})
	if len(due) != 0 {
		t.Fatal("矛盾订单绝不能被投递")
	}
}

func TestRefundWritesNegativeLedgerEntryOnce(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()

	d := newDeviceFixture(t, "fp-refund")
	account := register(t, e, d, "carol", "password123")
	printer, _ := e.svc.Printers.Upsert(ctx, UpsertPrinterInput{
		Code: "T10", Name: "测试机", CupsURI: "ipp://127.0.0.1:631/ipp/print",
		Status: "online", PriceSingleBWFen: 100, PaperSizes: []string{"A4"}, MaxCopies: 10,
	})
	file := uploadFile(t, e, account.User.ID)
	order := createOrder(t, e, account, printer.ID, file.ID, domain.DefaultPrintParams())

	if _, err := e.svc.Payments.Report(ctx, ReportInput{
		Report: &notifier.Report{
			EventID: "evt-refund", Source: notifier.SourceWeChatAndroid,
			Channel: domain.ChannelWeChat, OrderNo: order.OrderNo, AmountFen: order.AmountFen,
		},
		UserID: account.User.ID,
	}); err != nil {
		t.Fatalf("上报失败: %v", err)
	}

	if err := e.svc.Payments.Refund(ctx, RefundInput{OrderID: order.ID, Reason: "测试退款"}); err != nil {
		t.Fatalf("退款失败: %v", err)
	}

	spent, entries, _ := e.svc.Payments.LedgerSnapshot(ctx, account.User.ID, 10)
	if spent != 0 {
		t.Fatalf("退款后累计支出 = %d 分，期望 0", spent)
	}
	if len(entries) != 2 {
		t.Fatalf("应有支付与退款两条流水，实际 %d 条", len(entries))
	}

	err := e.svc.Payments.Refund(ctx, RefundInput{OrderID: order.ID})
	assertCode(t, err, "conflict")

	detail, _ := e.svc.Orders.Detail(ctx, account.User.ID, order.ID)
	if detail.Status != string(domain.OrderRefunded) {
		t.Fatalf("订单状态 = %s，期望 refunded", detail.Status)
	}
}

func TestAdminDeviceReleaseClearsEverythingAndFreesTheHardware(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()

	d := newDeviceFixture(t, "fp-release")
	account := register(t, e, d, "alice", "password123")

	if n := countRows(t, e, `SELECT COUNT(*) FROM device_nonces WHERE fingerprint = 'fp-release'`); n == 0 {
		t.Fatal("前置条件不成立：注册后该指纹下应当已有 nonce")
	}

	if err := e.svc.Devices.AdminReleaseDevice(ctx, ReleaseDeviceInput{
		DeviceID: account.Device.ID, Reason: "测试：换机", ActorType: "admin", ActorID: "admin-token",
	}); err != nil {
		t.Fatalf("管理员解绑失败: %v", err)
	}

	if _, err := e.svc.Auth.AuthenticateSession(ctx, account.Token); err == nil {
		t.Fatal("解绑后原会话应当失效")
	}
	if _, err := e.svc.Q.GetSessionByTokenHash(ctx, auth.HashToken(account.Token)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("解绑应当删除该设备的会话行，实际 %v", err)
	}

	released, err := e.svc.Q.GetDeviceByID(ctx, account.Device.ID)
	if err != nil {
		t.Fatalf("解绑后的行应当保留（订单外键指着它）: %v", err)
	}
	if released.Status != string(domain.DeviceReleased) {
		t.Fatalf("解绑后状态 = %s，期望 released", released.Status)
	}
	if released.Fingerprint == "fp-release" {
		t.Fatal("解绑必须让出指纹，否则这台机器永远只能登录、不能再注册")
	}
	if released.UserID != nil || released.PublicKey != "" {
		t.Fatalf("解绑必须清空归属与公钥固定，实际 user_id=%v public_key=%q",
			released.UserID, released.PublicKey)
	}
	if _, err := e.svc.Q.GetDeviceByFingerprint(ctx, "fp-release"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("已释放的指纹不应再被查到，实际 %v", err)
	}
	if n := countRows(t, e, `SELECT COUNT(*) FROM device_nonces WHERE fingerprint = 'fp-release'`); n != 0 {
		t.Fatalf("解绑应当清空该指纹的重放账本，仍有 %d 条", n)
	}

	bob := register(t, e, newDeviceFixture(t, "fp-release"), "bob", "password123")
	if bob.Device.ID == account.Device.ID {
		t.Fatal("注册应当新建一台设备，而不是复用旧行")
	}
	if bob.Device.Fingerprint != "fp-release" {
		t.Fatalf("新设备应重新占回该指纹，实际 %q", bob.Device.Fingerprint)
	}
	if bob.Device.UserID == nil || *bob.Device.UserID != bob.User.ID {
		t.Fatal("新设备应绑定到新账号")
	}

	requireErrorCode(t, e.svc.Devices.AdminReleaseDevice(ctx, ReleaseDeviceInput{
		DeviceID: account.Device.ID, Reason: "再来一次", ActorID: "admin-token",
	}), "conflict")
}

func TestReleasedDeviceLogsInAsBrandNewDevice(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()

	account := register(t, e, newDeviceFixture(t, "fp-relogin"), "alice", "password123")
	if err := e.svc.Devices.AdminReleaseDevice(ctx, ReleaseDeviceInput{
		DeviceID: account.Device.ID, Reason: "测试：设备丢失", ActorID: "admin-token",
	}); err != nil {
		t.Fatalf("管理员解绑失败: %v", err)
	}

	rekeyed := newDeviceFixture(t, "fp-relogin")
	result, err := e.svc.Auth.Login(ctx, "alice", "password123",
		rekeyed.signed(t, "POST", "/api/v1/auth/login", nil, *e.now, newNonce(t)))
	if err != nil {
		t.Fatalf("解绑后应当允许重新登录: %v", err)
	}
	if result.Device.ID == account.Device.ID {
		t.Fatal("重新登录应当新建一台设备，而不是复用旧行")
	}
	if result.Device.Fingerprint != "fp-relogin" {
		t.Fatalf("新设备应占回该指纹，实际 %q", result.Device.Fingerprint)
	}
	if result.Device.UserID == nil || *result.Device.UserID != account.User.ID {
		t.Fatal("重新登录后新设备应绑定到该账号")
	}

	if old, err := e.svc.Q.GetDeviceByID(ctx, account.Device.ID); err != nil ||
		old.Status != string(domain.DeviceReleased) {
		t.Fatalf("旧行应当保持已释放，实际 %+v，err=%v", old, err)
	}
}

func countRows(t *testing.T, e *env, query string, args ...any) int {
	t.Helper()
	var n int
	if err := e.svc.DB.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("统计失败: %v\n%s", err, query)
	}
	return n
}

func TestSessionIsBoundToTheDeviceItLoggedInOn(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()

	d1 := newDeviceFixture(t, "fp-sess-1")
	account := register(t, e, d1, "alice", "password123")

	d2 := newDeviceFixture(t, "fp-sess-2")
	if _, err := e.svc.Auth.Login(ctx, "alice", "password123",
		d2.signed(t, "POST", "/api/v1/auth/login", nil, *e.now, newNonce(t))); err != nil {
		t.Fatalf("第二台设备登录失败: %v", err)
	}

	session, err := e.svc.Auth.AuthenticateSession(ctx, account.Token)
	if err != nil {
		t.Fatalf("校验会话失败: %v", err)
	}
	if session.DeviceID() != account.Device.ID {
		t.Fatalf("会话应当记着登录它的那台设备，实际 %q（期望 %q）",
			session.DeviceID(), account.Device.ID)
	}
	other, err := e.svc.Auth.Login(ctx, "alice", "password123",
		d2.signed(t, "POST", "/api/v1/auth/login", nil, *e.now, newNonce(t)))
	if err != nil {
		t.Fatalf("第二台设备再登录失败: %v", err)
	}
	if other.Device.ID == account.Device.ID {
		t.Fatal("两台设备不应被视为同一台")
	}

	if err := e.svc.Devices.BlockDevice(ctx, account.Device.ID, "admin-1", "测试", ""); err != nil {
		t.Fatalf("停用设备失败: %v", err)
	}
	if _, err := e.svc.Auth.AuthenticateSession(ctx, account.Token); err == nil {
		t.Fatal("设备被停用后，它上面的会话应当失效")
	}

	if _, err := e.svc.Auth.AuthenticateSession(ctx, other.Token); err != nil {
		t.Fatalf("另一台设备的会话不应被牵连: %v", err)
	}
}

func TestUploadRejectsNonPDF(t *testing.T) {
	e := newEnv(t, nil)
	account := register(t, e, newDeviceFixture(t, "fp-upload"), "alice", "password123")

	_, err := e.svc.Files.Upload(context.Background(), UploadInput{
		UserID:   account.User.ID,
		Filename: "evil.txt",
		Reader:   strings.NewReader("hello world, not a pdf"),
	})
	assertCode(t, err, "unsupported_file_type")

	_, err = e.svc.Files.Upload(context.Background(), UploadInput{
		UserID:   account.User.ID,
		Filename: "empty.pdf",
		Reader:   strings.NewReader(""),
	})
	assertCode(t, err, "empty_file")
}

func TestUploadAcceptsClientPageCountOnlyAsFallback(t *testing.T) {
	e := newEnv(t, nil)
	account := register(t, e, newDeviceFixture(t, "fp-hint"), "alice", "password123")

	unrecognizable := "%PDF-1.7\n1 0 obj\n<< /Type /ObjStm >>\nendobj\n%%EOF"

	_, err := e.svc.Files.Upload(context.Background(), UploadInput{
		UserID: account.User.ID, Filename: "x.pdf", Reader: strings.NewReader(unrecognizable),
	})
	assertCode(t, err, "validation_error")

	view, err := e.svc.Files.Upload(context.Background(), UploadInput{
		UserID: account.User.ID, Filename: "x.pdf", Reader: strings.NewReader(unrecognizable),
		PageCountHint: 7,
	})
	if err != nil {
		t.Fatalf("提供页数兜底后应当成功: %v", err)
	}
	if view.PageCount != 7 {
		t.Fatalf("页数 = %d，期望采用客户端上报的 7", view.PageCount)
	}
}

func TestOrderNotFoundForOtherUsers(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()

	owner := register(t, e, newDeviceFixture(t, "fp-o1"), "owner", "password123")
	printer, _ := e.svc.Printers.Upsert(ctx, UpsertPrinterInput{
		Code: "T11", Name: "测试机", CupsURI: "ipp://127.0.0.1:631/ipp/print",
		Status: "online", PriceSingleBWFen: 100, PaperSizes: []string{"A4"}, MaxCopies: 10,
	})
	file := uploadFile(t, e, owner.User.ID)
	order := createOrder(t, e, owner, printer.ID, file.ID, domain.DefaultPrintParams())

	thief := register(t, e, newDeviceFixture(t, "fp-o2"), "thief", "password123")
	_, err := e.svc.Orders.Detail(ctx, thief.User.ID, order.ID)
	assertCode(t, err, "order_not_found")

	err = e.svc.Orders.Cancel(ctx, thief.User.ID, order.ID, "偷来的", "")
	assertCode(t, err, "order_not_found")
}

func TestFreeOrderStillRequiresManualRelease(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()

	account := register(t, e, newDeviceFixture(t, "fp-free"), "carol", "password123")
	printer, _ := e.svc.Printers.Upsert(ctx, UpsertPrinterInput{
		Code: "T12", Name: "免费机", CupsURI: "ipp://127.0.0.1:631/ipp/print",
		Status: "online", PriceSingleBWFen: 0, PriceSingleColorFen: 0, PaperSizes: []string{"A4"}, MaxCopies: 10,
	})
	file := uploadFile(t, e, account.User.ID)

	order := createOrder(t, e, account, printer.ID, file.ID, domain.DefaultPrintParams())
	if order.AmountFen != 0 {
		t.Fatalf("金额 = %d 分，期望 0", order.AmountFen)
	}
	if order.Status != string(domain.OrderAwaitingRelease) {
		t.Fatalf("免费订单状态 = %s，期望 awaiting_release（不该停在待支付）", order.Status)
	}
	if order.PaidAt == 0 {
		t.Fatal("免费订单也应有支付时间")
	}
	if !order.CanRelease {
		t.Fatal("免费订单应提示可以确认打印")
	}

	if _, err := e.svc.Q.GetJobByOrderID(ctx, order.ID); err == nil {
		t.Fatal("免费订单未确认前不应有打印任务")
	}

	released, err := releaseOrder(t, e, account.User.ID, order.ID)
	if err != nil {
		t.Fatalf("确认打印失败: %v", err)
	}
	if released.Status != string(domain.OrderQueued) {
		t.Fatalf("确认后状态 = %s，期望 queued", released.Status)
	}
	if released.JobStatus != string(domain.JobPending) {
		t.Fatalf("确认后任务状态 = %s，期望 pending", released.JobStatus)
	}
}
