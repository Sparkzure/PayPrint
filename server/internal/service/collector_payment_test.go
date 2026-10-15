package service

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/Sparkzure/PayPrint/server/internal/auth"
	"github.com/Sparkzure/PayPrint/server/internal/config"
	"github.com/Sparkzure/PayPrint/server/internal/database"
	"github.com/Sparkzure/PayPrint/server/internal/domain"
	"github.com/Sparkzure/PayPrint/server/internal/notifier"
	"github.com/Sparkzure/PayPrint/server/internal/store"
)

var testBaseTime = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

type paymentTestEnv struct {
	t   *testing.T
	db  *sql.DB
	svc *Services
	now time.Time

	siteA string
	siteB string
}

func newPaymentTestEnv(t *testing.T) *paymentTestEnv {
	t.Helper()
	ctx := context.Background()

	db, err := database.Open(
		filepath.Join(t.TempDir(), "test.db"), database.Options{MaxOpenConns: 1})
	if err != nil {
		t.Fatalf("打开测试库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatalf("迁移测试库: %v", err)
	}

	env := &paymentTestEnv{t: t, db: db, now: testBaseTime}
	env.svc = New(&Deps{
		DB: db,
		Q:  store.New(db),
		Cfg: &config.Config{
			SignatureSkew: 5 * time.Minute,

			StorageDir: t.TempDir(),
			WeChat:     config.WeChatConfig{MaxAttemp: 3},
		},
		Triggers: notifier.NewRegistry(),
		Queriers: notifier.NewQueriers(nil),
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:      func() time.Time { return env.now },
	})

	env.siteA = "printer-a"
	env.siteB = "printer-b"
	env.createPrinter(env.siteA, "A")
	env.createPrinter(env.siteB, "B")
	return env
}

func (e *paymentTestEnv) ctx() context.Context { return context.Background() }

func (e *paymentTestEnv) mustExec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.db.ExecContext(e.ctx(), query, args...); err != nil {
		e.t.Fatalf("执行 SQL 失败: %v\n%s", err, query)
	}
}

func (e *paymentTestEnv) createPrinter(id, code string) {
	e.t.Helper()
	e.mustExec(`INSERT INTO printers (id, code, name, cups_uri, location, status,
		price_single_bw_fen, price_single_color_fen, price_duplex_bw_fen, price_duplex_color_fen,
		supports_color, supports_duplex, paper_sizes,
		max_copies, sort_order, created_at, updated_at)
		VALUES (?, ?, ?, ?, '', 'online', 50, 200, 100, 400, 0, 0, '["A4"]', 99, 0, ?, ?)`,
		id, code, "打印机"+code, "ipp://127.0.0.1:631/printers/"+code,
		e.now.Unix(), e.now.Unix())
}

func (e *paymentTestEnv) createUser(id string, role domain.UserRole) string {
	e.t.Helper()
	e.mustExec(`INSERT INTO users (id, username, username_ci, password_hash,
		phone, role, status, created_at, updated_at)
		VALUES (?, ?, ?, 'x', '', ?, 'active', ?, ?)`,
		id, id, id, string(role), e.now.Unix(), e.now.Unix())
	return id
}

func (e *paymentTestEnv) createDevice(id, fingerprint, ownerID string) store.Device {
	e.t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		e.t.Fatalf("生成设备密钥: %v", err)
	}
	encoded, err := auth.EncodePublicKey(&priv.PublicKey)
	if err != nil {
		e.t.Fatalf("编码设备公钥: %v", err)
	}
	return e.createDeviceWithKey(id, fingerprint, ownerID, encoded)
}

func (e *paymentTestEnv) createDeviceWithKey(
	id, fingerprint, ownerID, publicKey string,
) store.Device {
	e.t.Helper()
	e.mustExec(`INSERT INTO devices (id, fingerprint, public_key, public_key_alg, status,
		user_id, bound_at, label, platform, app_version, first_seen_at, last_seen_at,
		revoked_at, revoke_reason, created_at, updated_at)
		VALUES (?, ?, ?, 'ecdsa-p256', 'active', ?, ?, '', '', '', ?, ?, 0, '', ?, ?)`,
		id, fingerprint, publicKey, ownerID,
		e.now.Unix(), e.now.Unix(), e.now.Unix(), e.now.Unix(), e.now.Unix())
	return e.device(id)
}

func (e *paymentTestEnv) device(id string) store.Device {
	e.t.Helper()
	dev, err := e.svc.Q.GetDeviceByID(e.ctx(), id)
	if err != nil {
		e.t.Fatalf("读取设备 %s: %v", id, err)
	}
	return dev
}

func (e *paymentTestEnv) issueCollector(printerID, label string) *IssuedCollectorView {
	e.t.Helper()
	printer, err := e.svc.Q.GetPrinterByID(e.ctx(), printerID)
	if err != nil {
		e.t.Fatalf("读取打印机 %s: %v", printerID, err)
	}
	view, err := e.svc.Collectors.Issue(e.ctx(), IssueCollectorInput{
		PrinterCode: printer.Code,
		Label:       label,
		ActorID:     "test-admin",
		IP:          "127.0.0.1",
	})
	if err != nil {
		e.t.Fatalf("签发收款凭据: %v", err)
	}
	if view.Token == "" {
		e.t.Fatal("签发收款凭据必须返回明文令牌（只此一次）")
	}
	return view
}

func (e *paymentTestEnv) issueCollectorCredential(printerID, label string) string {
	e.t.Helper()
	return e.issueCollector(printerID, label).Token
}

func (e *paymentTestEnv) createFile(id, ownerID string) string {
	e.t.Helper()
	e.mustExec(`INSERT INTO print_files (id, user_id, original_name, content_type,
		size_bytes, sha256, storage_path, page_count, created_at)
		VALUES (?, ?, 'doc.pdf', 'application/pdf', 1024, 'sha', '/tmp/doc.pdf', 1, ?)`,
		id, ownerID, e.now.Unix())
	return id
}

type orderSeed struct {
	id        string
	no        string
	userID    string
	printerID string
	fileID    string
	amountFen int64
	status    domain.OrderStatus
	createdAt int64
	expiresAt int64
	closedAt  int64
}

func (e *paymentTestEnv) createOrder(s orderSeed) store.Order {
	e.t.Helper()
	e.mustExec(`INSERT INTO orders (id, order_no, user_id, printer_id, file_id,
		status, amount_fen, currency, title, params_json, cost_json, sides_per_copy,
		sheets_per_copy, is_test, created_at, updated_at, expires_at, paid_at,
		completed_at, closed_at, close_reason)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'CNY', '', '{}', '{}', 1, 1, 0, ?, ?, ?, 0, 0, ?, '')`,
		s.id, s.no, s.userID, s.printerID, s.fileID, string(s.status),
		s.amountFen, s.createdAt, s.createdAt, s.expiresAt, s.closedAt)
	return e.order(s.id)
}

func (e *paymentTestEnv) order(id string) store.Order {
	e.t.Helper()
	o, err := e.svc.Q.GetOrderByID(e.ctx(), id)
	if err != nil {
		e.t.Fatalf("读取订单 %s: %v", id, err)
	}
	return o
}

func (e *paymentTestEnv) seedUserOrder(
	suffix string, printerID string, amountFen int64, status domain.OrderStatus,
	createdAt, expiresAt int64,
) (store.Order, string) {
	e.t.Helper()
	userID := e.createUser("user-"+suffix, domain.RoleUser)
	fileID := e.createFile("file-"+suffix, userID)
	order := e.createOrder(orderSeed{
		id: "order-" + suffix, no: "NO" + suffix,
		userID: userID, printerID: printerID, fileID: fileID,
		amountFen: amountFen, status: status,
		createdAt: createdAt, expiresAt: expiresAt,
	})
	return order, userID
}

func collectorReport(eventID string, amountFen, paidAt int64) *notifier.Report {
	return &notifier.Report{
		EventID:   eventID,
		Source:    notifier.SourceWeChatAndroid,
		Channel:   domain.ChannelWeChat,
		AmountFen: amountFen,
		PaidAt:    paidAt,
		Timestamp: paidAt,
		Nonce:     eventID,
	}
}

func requireErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("期望错误码 %s，实际成功", code)
	}
	appErr, ok := domain.AsAppError(err)
	if !ok {
		t.Fatalf("期望应用错误 %s，实际: %v", code, err)
	}
	if appErr.Code != code {
		t.Fatalf("期望错误码 %s，实际 %s（%v）", code, appErr.Code, err)
	}
}

func (e *paymentTestEnv) collectorEnv() string {
	e.t.Helper()
	token := e.issueCollectorCredential(e.siteA, "A 点收款机")
	identity, err := e.svc.Collectors.Authenticate(e.ctx(), token)
	if err != nil {
		e.t.Fatalf("用收款凭据换身份: %v", err)
	}
	return identity.PrinterID
}

func TestCollectorReportMatchesUniqueOrder(t *testing.T) {
	e := newPaymentTestEnv(t)
	collectorSite := e.collectorEnv()
	order, _ := e.seedUserOrder("1", e.siteA, 60,
		domain.OrderAwaitingPayment, e.now.Add(-time.Minute).Unix(), e.now.Add(10*time.Minute).Unix())

	res, err := e.svc.Payments.Report(e.ctx(), ReportInput{
		Report:             collectorReport("evt-1", 60, e.now.Unix()),
		CollectorPrinterID: collectorSite,
	})
	if err != nil {
		t.Fatalf("唯一候选应当放行: %v", err)
	}
	if !res.Accepted || res.OrderNo != order.OrderNo {
		t.Fatalf("返回值不正确: %+v", res)
	}

	if res.JobQueued {
		t.Fatal("默认部署不应自动放行打印")
	}
	if got := e.order(order.ID); got.Status != string(domain.OrderAwaitingRelease) {
		t.Fatalf("订单状态应为 awaiting_release，实际 %s", got.Status)
	}

	ev, err := e.svc.Q.GetPaymentEventByEventID(e.ctx(), "evt-1")
	if err != nil {
		t.Fatalf("读取支付事件: %v", err)
	}
	if ev.OrderNo != order.OrderNo {
		t.Fatalf("事件应回填订单号 %s，实际 %q", order.OrderNo, ev.OrderNo)
	}
	if ev.Status != string(domain.PaymentAccepted) {
		t.Fatalf("事件状态应为 accepted，实际 %s", ev.Status)
	}
}

func TestCollectorReportRejectsAmbiguousAmount(t *testing.T) {
	e := newPaymentTestEnv(t)
	collectorSite := e.collectorEnv()
	first, _ := e.seedUserOrder("1", e.siteA, 60,
		domain.OrderAwaitingPayment, e.now.Add(-3*time.Minute).Unix(), e.now.Add(10*time.Minute).Unix())
	second, _ := e.seedUserOrder("2", e.siteA, 60,
		domain.OrderAwaitingPayment, e.now.Add(-2*time.Minute).Unix(), e.now.Add(10*time.Minute).Unix())

	_, err := e.svc.Payments.Report(e.ctx(), ReportInput{
		Report:             collectorReport("evt-amb", 60, e.now.Unix()),
		CollectorPrinterID: collectorSite,
	})
	requireErrorCode(t, err, "payment_ambiguous")

	for _, o := range []store.Order{first, second} {
		if got := e.order(o.ID); got.Status != string(domain.OrderAwaitingPayment) {
			t.Fatalf("歧义被拒时订单 %s 不应被改动，实际 %s", o.OrderNo, got.Status)
		}
	}

	ev, err := e.svc.Q.GetPaymentEventByEventID(e.ctx(), "evt-amb")
	if err != nil {
		t.Fatalf("歧义上报必须留痕: %v", err)
	}

	if ev.Status != string(domain.PaymentUnresolved) {
		t.Fatalf("歧义上报应记为 unresolved，实际 %s", ev.Status)
	}
}

func TestCollectorReportDoesNotCrossSites(t *testing.T) {
	e := newPaymentTestEnv(t)
	collectorSite := e.collectorEnv()
	other, _ := e.seedUserOrder("other", e.siteB, 60,
		domain.OrderAwaitingPayment, e.now.Add(-time.Minute).Unix(), e.now.Add(10*time.Minute).Unix())

	_, err := e.svc.Payments.Report(e.ctx(), ReportInput{
		Report:             collectorReport("evt-cross", 60, e.now.Unix()),
		CollectorPrinterID: collectorSite,
	})
	requireErrorCode(t, err, "payment_unmatched")

	if got := e.order(other.ID); got.Status != string(domain.OrderAwaitingPayment) {
		t.Fatalf("跨打印机的订单不应被改动，实际 %s", got.Status)
	}
}

func TestCollectorReportLatePaymentNamesExpiredOrder(t *testing.T) {
	e := newPaymentTestEnv(t)
	collectorSite := e.collectorEnv()

	order, _ := e.seedUserOrder("late", e.siteA, 60, domain.OrderExpired,
		e.now.Add(-40*time.Minute).Unix(), e.now.Add(-25*time.Minute).Unix())
	e.mustExec(`UPDATE orders SET closed_at = ? WHERE id = ?`,
		e.now.Add(-25*time.Minute).Unix(), order.ID)

	_, err := e.svc.Payments.Report(e.ctx(), ReportInput{
		Report:             collectorReport("evt-late", 60, e.now.Add(-24*time.Minute).Unix()),
		CollectorPrinterID: collectorSite,
	})
	requireErrorCode(t, err, "order_expired")

	ev, err := e.svc.Q.GetPaymentEventByEventID(e.ctx(), "evt-late")
	if err != nil {
		t.Fatalf("超时到账必须留痕: %v", err)
	}
	if ev.OrderID == nil || *ev.OrderID != order.ID {
		t.Fatalf("超时到账应关联到订单 %s", order.OrderNo)
	}
	if got := e.order(order.ID); got.Status != string(domain.OrderExpired) {
		t.Fatalf("超时订单不应被重新放行，实际 %s", got.Status)
	}
}

func TestCollectorReportLatePaymentNamesUnsweptOrder(t *testing.T) {
	e := newPaymentTestEnv(t)
	collectorSite := e.collectorEnv()
	order, _ := e.seedUserOrder("unswept", e.siteA, 60, domain.OrderAwaitingPayment,
		e.now.Add(-40*time.Minute).Unix(), e.now.Add(-25*time.Minute).Unix())

	_, err := e.svc.Payments.Report(e.ctx(), ReportInput{
		Report:             collectorReport("evt-unswept", 60, e.now.Add(-24*time.Minute).Unix()),
		CollectorPrinterID: collectorSite,
	})
	requireErrorCode(t, err, "order_expired")

	ev, err := e.svc.Q.GetPaymentEventByEventID(e.ctx(), "evt-unswept")
	if err != nil {
		t.Fatalf("超时到账必须留痕: %v", err)
	}
	if ev.OrderID == nil || *ev.OrderID != order.ID {
		t.Fatalf("应当指认订单 %s，实际 %v", order.OrderNo, ev.OrderID)
	}
}

func TestCollectorCannotClaimOrderFromAnotherSite(t *testing.T) {
	e := newPaymentTestEnv(t)
	collectorSite := e.collectorEnv()
	other, _ := e.seedUserOrder("b1", e.siteB, 60,
		domain.OrderAwaitingPayment, e.now.Add(-time.Minute).Unix(), e.now.Add(10*time.Minute).Unix())

	report := collectorReport("evt-claim", 60, e.now.Unix())
	report.OrderNo = other.OrderNo

	_, err := e.svc.Payments.Report(e.ctx(), ReportInput{
		Report:             report,
		CollectorPrinterID: collectorSite,
	})
	requireErrorCode(t, err, "order_not_owned")

	if got := e.order(other.ID); got.Status != string(domain.OrderAwaitingPayment) {
		t.Fatalf("越权订单不应被改动，实际 %s", got.Status)
	}
}

func TestCollectorCanClaimOrderFromItsOwnSite(t *testing.T) {
	e := newPaymentTestEnv(t)
	collectorSite := e.collectorEnv()
	order, _ := e.seedUserOrder("a1", e.siteA, 60,
		domain.OrderAwaitingPayment, e.now.Add(-time.Minute).Unix(), e.now.Add(10*time.Minute).Unix())

	report := collectorReport("evt-own", 60, e.now.Unix())
	report.OrderNo = order.OrderNo

	res, err := e.svc.Payments.Report(e.ctx(), ReportInput{
		Report:             report,
		CollectorPrinterID: collectorSite,
	})
	if err != nil {
		t.Fatalf("本打印机订单应当放行: %v", err)
	}
	if !res.Accepted {
		t.Fatalf("应当被接受: %+v", res)
	}
}

func TestConsumerAccountWithoutOrderNoIsRejected(t *testing.T) {
	e := newPaymentTestEnv(t)
	order, userID := e.seedUserOrder("c1", e.siteA, 60,
		domain.OrderAwaitingPayment, e.now.Add(-time.Minute).Unix(), e.now.Add(10*time.Minute).Unix())

	_, err := e.svc.Payments.Report(e.ctx(), ReportInput{
		Report: collectorReport("evt-consumer", 60, e.now.Unix()),
		UserID: userID,
	})
	requireErrorCode(t, err, "report_needs_order_no")

	if got := e.order(order.ID); got.Status != string(domain.OrderAwaitingPayment) {
		t.Fatalf("订单不应被改动，实际 %s", got.Status)
	}
}

func TestCollectorReportReplayIsIdempotent(t *testing.T) {
	e := newPaymentTestEnv(t)
	collectorSite := e.collectorEnv()
	order, _ := e.seedUserOrder("r1", e.siteA, 60,
		domain.OrderAwaitingPayment, e.now.Add(-time.Minute).Unix(), e.now.Add(10*time.Minute).Unix())

	if _, err := e.svc.Payments.Report(e.ctx(), ReportInput{
		Report:             collectorReport("evt-r1", 60, e.now.Unix()),
		CollectorPrinterID: collectorSite,
	}); err != nil {
		t.Fatalf("首次上报应当放行: %v", err)
	}

	res, err := e.svc.Payments.Report(e.ctx(), ReportInput{
		Report:             collectorReport("evt-r1-again", 60, e.now.Unix()),
		CollectorPrinterID: collectorSite,
	})
	if err != nil {
		t.Fatalf("重复上报不应报错: %v", err)
	}
	if !res.Accepted || !res.Duplicate {
		t.Fatalf("重复上报应按幂等成功返回: %+v", res)
	}

	var jobs int64
	if err := e.db.QueryRowContext(e.ctx(),
		`SELECT COUNT(*) FROM print_jobs WHERE order_id = ?`, order.ID).Scan(&jobs); err != nil {
		t.Fatalf("统计打印任务: %v", err)
	}
	if jobs != 0 {
		t.Fatalf("默认部署下不应创建打印任务，实际 %d 条", jobs)
	}

	res, err = e.svc.Payments.Report(e.ctx(), ReportInput{
		Report:             collectorReport("evt-r1", 60, e.now.Unix()),
		CollectorPrinterID: collectorSite,
	})
	if err != nil || !res.Duplicate {
		t.Fatalf("同一 event_id 重投应当按幂等返回: %+v %v", res, err)
	}
}

func TestCollectorCredentialLifecycle(t *testing.T) {
	e := newPaymentTestEnv(t)
	ctx := e.ctx()

	if _, err := e.svc.Collectors.Authenticate(ctx, "   "); err == nil {
		t.Fatal("空令牌必须被拒")
	}
	if _, err := e.svc.Collectors.Authenticate(ctx, "not-a-real-token"); err == nil {
		t.Fatal("无效令牌必须被拒")
	}

	issued := e.issueCollector(e.siteA, "A 点收款机")
	identity, err := e.svc.Collectors.Authenticate(ctx, issued.Token)
	if err != nil {
		t.Fatalf("有效凭据应当通过鉴权: %v", err)
	}
	if identity.PrinterID != e.siteA {
		t.Fatalf("凭据应绑定签发时的打印机 A，实际 %q", identity.PrinterID)
	}

	var hash string
	if err := e.db.QueryRowContext(ctx,
		`SELECT token_hash FROM collector_credentials WHERE id = ?`,
		issued.ID).Scan(&hash); err != nil {
		t.Fatalf("读取凭据散列: %v", err)
	}
	if hash == issued.Token {
		t.Fatal("库里不能存明文令牌")
	}
	if hash != auth.HashToken(issued.Token) {
		t.Fatal("库里应当存令牌的 SHA-256")
	}

	views, err := e.svc.Collectors.List(ctx, 10)
	if err != nil {
		t.Fatalf("列出凭据: %v", err)
	}
	if len(views) != 1 || views[0].PrinterCode != "A" || views[0].Revoked {
		t.Fatalf("列表结果不正确: %+v", views)
	}

	if err := e.svc.Collectors.Revoke(ctx, issued.ID, "换机", "test-admin", "127.0.0.1"); err != nil {
		t.Fatalf("吊销凭据: %v", err)
	}
	_, err = e.svc.Collectors.Authenticate(ctx, issued.Token)
	requireErrorCode(t, err, "collector_credential_invalid")

	err = e.svc.Collectors.Revoke(ctx, issued.ID, "", "test-admin", "127.0.0.1")
	requireErrorCode(t, err, "collector_credential_not_found")
}

func TestBannedAccountDeviceLosesAccess(t *testing.T) {
	e := newPaymentTestEnv(t)
	userID := e.createUser("banned-1", domain.RoleUser)

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成密钥: %v", err)
	}
	encoded, err := auth.EncodePublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("编码公钥: %v", err)
	}
	dev := e.createDeviceWithKey("dev-banned", "fp-banned", userID, encoded)

	if _, err := e.authenticate(priv, dev, "nonce-0000000000000001"); err != nil {
		t.Fatalf("账号正常时应当通过设备鉴权: %v", err)
	}

	if _, err := e.db.ExecContext(e.ctx(),
		`UPDATE users SET status = 'banned' WHERE id = ?`, userID); err != nil {
		t.Fatalf("封禁账号: %v", err)
	}

	_, err = e.authenticate(priv, dev, "nonce-0000000000000002")
	requireErrorCode(t, err, "account_banned")
}

func (e *paymentTestEnv) authenticate(
	priv *ecdsa.PrivateKey, dev store.Device, nonce string,
) (*DeviceIdentity, error) {
	e.t.Helper()
	body := []byte(`{"event_id":"` + nonce + `","amount_fen":60}`)
	ts := e.now.Unix()
	in := DeviceAuthInput{
		Method:      "POST",
		Path:        "/api/v1/payment/report",
		Body:        body,
		Fingerprint: dev.Fingerprint,
		Timestamp:   ts,
		Nonce:       nonce,
	}
	canonical := auth.CanonicalString(in.Method, in.Path, "", ts, nonce, auth.BodyHash(body))
	sig, err := auth.SignForTest(priv, []byte(canonical))
	if err != nil {
		e.t.Fatalf("签名: %v", err)
	}
	in.Signature = sig

	return e.svc.Auth.AuthenticateDevice(e.ctx(), in, DeviceAuthOptions{RequireBound: true})
}

func TestIssueCollectorRequiresExistingPrinter(t *testing.T) {
	e := newPaymentTestEnv(t)
	ctx := e.ctx()

	_, err := e.svc.Collectors.Issue(ctx, IssueCollectorInput{
		PrinterCode: "不存在的打印机", ActorID: "test-admin",
	})
	requireErrorCode(t, err, "printer_not_found")

	_, err = e.svc.Collectors.Issue(ctx, IssueCollectorInput{
		PrinterCode: "   ", ActorID: "test-admin",
	})
	requireErrorCode(t, err, "validation_error")

	view, err := e.svc.Collectors.Issue(ctx, IssueCollectorInput{
		PrinterCode: "A", ActorID: "test-admin", IP: "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("签发收款凭据失败: %v", err)
	}
	if view.PrinterCode != "A" || view.Token == "" {
		t.Fatalf("签发结果不正确: %+v", view)
	}
	if view.Label == "" {
		t.Fatal("未指定名字时应当用打印机名兜底，否则列表里认不出是哪台机器")
	}

	userID := e.createUser("dev-owner", domain.RoleUser)
	dev := e.createDevice("dev-plain", "fp-plain", userID)
	if err := e.svc.Devices.AdminReleaseDevice(ctx, ReleaseDeviceInput{
		DeviceID: dev.ID, ActorID: "test-admin",
	}); err != nil {
		t.Fatalf("解绑设备: %v", err)
	}

	if _, err := e.svc.Collectors.Authenticate(ctx, view.Token); err != nil {
		t.Fatalf("解绑设备不应影响收款凭据: %v", err)
	}
}

func TestCollectorReportLedgerWrittenOnce(t *testing.T) {
	e := newPaymentTestEnv(t)
	collectorSite := e.collectorEnv()
	order, _ := e.seedUserOrder("l1", e.siteA, 60,
		domain.OrderAwaitingPayment, e.now.Add(-time.Minute).Unix(), e.now.Add(10*time.Minute).Unix())

	for _, eventID := range []string{"evt-l1", "evt-l1-second"} {
		if _, err := e.svc.Payments.Report(e.ctx(), ReportInput{
			Report:             collectorReport(eventID, 60, e.now.Unix()),
			CollectorPrinterID: collectorSite,
		}); err != nil {
			t.Fatalf("上报 %s 失败: %v", eventID, err)
		}
	}

	var entries, amount int64
	if err := e.db.QueryRowContext(e.ctx(),
		`SELECT COUNT(*), COALESCE(SUM(amount_fen), 0) FROM ledger_entries WHERE order_id = ?`,
		order.ID).Scan(&entries, &amount); err != nil {
		t.Fatalf("统计记账流水: %v", err)
	}
	if entries != 1 || amount != order.AmountFen {
		t.Fatalf("记账应当只发生一次（实际 %d 条、%d 分）", entries, amount)
	}
}
