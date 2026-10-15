package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Sparkzure/PayPrint/server/internal/config"
	"github.com/Sparkzure/PayPrint/server/internal/domain"
	"github.com/Sparkzure/PayPrint/server/internal/ipp"
	"github.com/Sparkzure/PayPrint/server/internal/notifier"
	"github.com/Sparkzure/PayPrint/server/internal/store"
)

type stubProber struct {
	err          error
	notAccepting bool
	state        string
}

func (s stubProber) GetPrinter(context.Context, string) (*ipp.PrinterInfo, error) {
	if s.err != nil {
		return nil, s.err
	}
	state := s.state
	if state == "" {
		state = "idle"
	}
	return &ipp.PrinterInfo{
		State:         state,
		RawState:      ipp.PrinterStateCode(state),
		AcceptingJobs: !s.notAccepting,
		MakeAndModel:  "stub",
	}, nil
}

func reportPayment(
	t *testing.T, e *env, userID, orderNo, eventID string, amount int64,
) (*ReportResult, error) {
	t.Helper()
	ctx := context.Background()

	return e.svc.Payments.Report(ctx, ReportInput{
		Report: &notifier.Report{
			EventID:   eventID,
			Source:    notifier.SourceWeChatAndroid,
			Channel:   domain.ChannelWeChat,
			OrderNo:   orderNo,
			AmountFen: amount,
			PaidAt:    e.now.Unix(),
		},
		UserID: userID,
	})
}

func releaseOrder(t *testing.T, e *env, userID, orderID string) (*OrderView, error) {
	t.Helper()
	return e.svc.Orders.Release(context.Background(), ReleaseInput{
		UserID:  userID,
		OrderID: orderID,
	})
}

func setupAwaitingRelease(t *testing.T, mutate func(*config.Config)) (*env, *AuthResult, *OrderView, string) {
	t.Helper()
	e := newEnv(t, mutate)
	ctx := context.Background()

	d := newDeviceFixture(t, "fp-release")
	account := register(t, e, d, "carol", "password123")
	printer, err := e.svc.Printers.Upsert(ctx, UpsertPrinterInput{
		Code: "R01", Name: "放行测试机", CupsURI: "ipp://127.0.0.1:631/ipp/print",
		Status: "online", PriceSingleBWFen: 100, PaperSizes: []string{"A4"}, MaxCopies: 10,
	})
	if err != nil {
		t.Fatalf("登记打印机: %v", err)
	}
	file := uploadFile(t, e, account.User.ID)
	order := createOrder(t, e, account, printer.ID, file.ID, domain.DefaultPrintParams())

	if _, err := reportPayment(t, e, account.User.ID, order.OrderNo, "evt-"+order.OrderNo, order.AmountFen); err != nil {
		t.Fatalf("支付上报失败: %v", err)
	}

	detail, err := e.svc.Orders.Detail(ctx, account.User.ID, order.ID)
	if err != nil {
		t.Fatalf("查询订单失败: %v", err)
	}
	if detail.Status != string(domain.OrderAwaitingRelease) {
		t.Fatalf("前置状态 = %s，期望 awaiting_release", detail.Status)
	}
	return e, account, detail, printer.ID
}

func TestReleaseCreatesExactlyOneJob(t *testing.T) {
	e, account, order, _ := setupAwaitingRelease(t, nil)
	ctx := context.Background()

	if _, err := e.svc.Q.GetJobByOrderID(ctx, order.ID); err == nil {
		t.Fatal("放行前不应存在打印任务")
	}

	view, err := releaseOrder(t, e, account.User.ID, order.ID)
	if err != nil {
		t.Fatalf("确认打印失败: %v", err)
	}
	if !view.JustReleased {
		t.Fatal("放行的响应应标记 just_released")
	}
	if view.Status != string(domain.OrderQueued) {
		t.Fatalf("状态 = %s，期望 queued", view.Status)
	}

	job, err := e.svc.Q.GetJobByOrderID(ctx, order.ID)
	if err != nil {
		t.Fatalf("应当存在打印任务: %v", err)
	}
	if job.Status != string(domain.JobPending) {
		t.Fatalf("任务状态 = %s，期望 pending", job.Status)
	}
	if job.PrinterID != order.PrinterID {
		t.Fatal("任务应绑定订单指定的打印机")
	}
}

func TestReleaseRejectsForeignOrder(t *testing.T) {
	e, _, order, _ := setupAwaitingRelease(t, nil)

	thief := register(t, e, newDeviceFixture(t, "fp-thief"), "dave", "password123")
	_, err := releaseOrder(t, e, thief.User.ID, order.ID)

	assertCode(t, err, "order_not_found")
}

func TestReleaseFromAnotherDeviceOfTheSameAccountWorks(t *testing.T) {
	e, account, order, _ := setupAwaitingRelease(t, nil)

	other := newDeviceFixture(t, "fp-second-device")
	result, err := e.svc.Auth.Login(context.Background(), "carol", "password123",
		other.signed(t, "POST", "/api/v1/auth/login", nil, *e.now, newNonce(t)))
	if err != nil {
		t.Fatalf("第二台设备登录失败: %v", err)
	}
	if result.User.ID != account.User.ID || result.Device.ID == account.Device.ID {
		t.Fatalf("应当是同一账号的另一台设备，实际 %+v", result.Device)
	}

	if _, err := releaseOrder(t, e, account.User.ID, order.ID); err != nil {
		t.Fatalf("同一账号换设备后应当仍能确认打印: %v", err)
	}

	job, err := e.svc.Q.GetJobByOrderID(context.Background(), order.ID)
	if err != nil {
		t.Fatalf("确认打印后应当有打印任务: %v", err)
	}
	if job.Status != string(domain.JobPending) {
		t.Fatalf("任务状态 = %s，期望 pending", job.Status)
	}
}

func TestReleaseRejectsUnpaidOrder(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()

	account := register(t, e, newDeviceFixture(t, "fp-unpaid"), "carol", "password123")
	printer, _ := e.svc.Printers.Upsert(ctx, UpsertPrinterInput{
		Code: "R02", Name: "测试机", CupsURI: "ipp://127.0.0.1:631/ipp/print",
		Status: "online", PriceSingleBWFen: 100, PaperSizes: []string{"A4"}, MaxCopies: 10,
	})
	file := uploadFile(t, e, account.User.ID)
	order := createOrder(t, e, account, printer.ID, file.ID, domain.DefaultPrintParams())

	_, err := releaseOrder(t, e, account.User.ID, order.ID)
	assertCode(t, err, "order_not_releasable")
	if _, err := e.svc.Q.GetJobByOrderID(ctx, order.ID); err == nil {
		t.Fatal("未支付的订单不应产生打印任务")
	}
}

func TestReleaseRejectsCancelledOrder(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()

	account := register(t, e, newDeviceFixture(t, "fp-cancel"), "carol", "password123")
	printer, _ := e.svc.Printers.Upsert(ctx, UpsertPrinterInput{
		Code: "R03", Name: "测试机", CupsURI: "ipp://127.0.0.1:631/ipp/print",
		Status: "online", PriceSingleBWFen: 100, PaperSizes: []string{"A4"}, MaxCopies: 10,
	})
	file := uploadFile(t, e, account.User.ID)
	order := createOrder(t, e, account, printer.ID, file.ID, domain.DefaultPrintParams())

	if err := e.svc.Orders.Cancel(ctx, account.User.ID, order.ID, "", ""); err != nil {
		t.Fatalf("取消订单失败: %v", err)
	}
	_, err := releaseOrder(t, e, account.User.ID, order.ID)
	assertCode(t, err, "order_not_releasable")
}

func TestReleaseRejectsUnreachablePrinter(t *testing.T) {
	e, account, order, _ := setupAwaitingRelease(t, nil)
	ctx := context.Background()

	e.svc.Prober = stubProber{err: errors.New("connection refused")}

	_, err := releaseOrder(t, e, account.User.ID, order.ID)
	assertCode(t, err, "printer_offline")

	detail, _ := e.svc.Orders.Detail(ctx, account.User.ID, order.ID)
	if detail.Status != string(domain.OrderAwaitingRelease) {
		t.Fatalf("探测失败后状态 = %s，期望仍为 awaiting_release", detail.Status)
	}
	if !detail.CanRelease {
		t.Fatal("探测失败后应仍然允许重试确认打印")
	}

	e.svc.Prober = stubProber{}
	if _, err := releaseOrder(t, e, account.User.ID, order.ID); err != nil {
		t.Fatalf("打印机恢复后应当可以放行: %v", err)
	}
}

func TestReleaseRejectsPausedPrinter(t *testing.T) {
	e, account, order, _ := setupAwaitingRelease(t, nil)

	e.svc.Prober = stubProber{state: "stopped"}
	_, err := releaseOrder(t, e, account.User.ID, order.ID)
	assertCode(t, err, "printer_offline")

	e.svc.Prober = stubProber{notAccepting: true}
	_, err = releaseOrder(t, e, account.User.ID, order.ID)
	assertCode(t, err, "printer_offline")
}

func setReleaseTTL(t *testing.T, e *env, d time.Duration) {
	t.Helper()
	if _, err := e.svc.Settings.Update(context.Background(), UpdateSettingsInput{
		Set: map[string]string{SettingReleaseTTL: d.String()},
	}); err != nil {
		t.Fatalf("设置确认时限: %v", err)
	}
}

func TestReleaseWindowTimeoutClosesOrder(t *testing.T) {
	e, account, order, _ := setupAwaitingRelease(t, nil)
	setReleaseTTL(t, e, 10*time.Minute)
	ctx := context.Background()

	e.advance(11 * time.Minute)

	_, err := releaseOrder(t, e, account.User.ID, order.ID)
	assertCode(t, err, "release_window_closed")

	detail, _ := e.svc.Orders.Detail(ctx, account.User.ID, order.ID)
	if detail.Status != string(domain.OrderRefundPending) {
		t.Fatalf("超时后状态 = %s，期望 refund_pending（钱已收，必须有人退）", detail.Status)
	}
	if _, err := e.svc.Q.GetJobByOrderID(ctx, order.ID); err == nil {
		t.Fatal("超时订单不应产生打印任务")
	}
}

func TestExpireStaleReleasesSweepsUnconfirmedOrders(t *testing.T) {
	e, account, order, _ := setupAwaitingRelease(t, nil)
	setReleaseTTL(t, e, 10*time.Minute)
	ctx := context.Background()

	if n, err := e.svc.Orders.ExpireStaleReleases(ctx, 100); err != nil || n != 0 {
		t.Fatalf("未到期不应回收，n=%d err=%v", n, err)
	}

	e.advance(20 * time.Minute)

	n, err := e.svc.Orders.ExpireStaleReleases(ctx, 100)
	if err != nil {
		t.Fatalf("回收失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("回收数量 = %d，期望 1", n)
	}

	stored, err := e.svc.Q.GetOrderByID(ctx, order.ID)
	if err != nil {
		t.Fatalf("读取订单失败: %v", err)
	}
	if stored.Status != string(domain.OrderRefundPending) {
		t.Fatalf("回收后状态 = %s，期望 refund_pending", stored.Status)
	}
	if stored.CloseReason == "" {
		t.Fatal("回收应当写清原因，便于人工核对")
	}

	if n, err := e.svc.Orders.ExpireStaleReleases(ctx, 100); err != nil || n != 0 {
		t.Fatalf("重复回收应无效果，n=%d err=%v", n, err)
	}

	_, err = releaseOrder(t, e, account.User.ID, order.ID)
	assertCode(t, err, "order_not_releasable")
}

func TestReleaseTTLZeroDisablesSweep(t *testing.T) {
	e, account, order, _ := setupAwaitingRelease(t, nil)
	setReleaseTTL(t, e, 0)
	ctx := context.Background()

	e.advance(72 * time.Hour)

	n, err := e.svc.Orders.ExpireStaleReleases(ctx, 100)
	if err != nil {
		t.Fatalf("回收失败: %v", err)
	}
	if n != 0 {
		t.Fatalf("关闭回收后不应有订单被处理，n=%d", n)
	}

	if _, err := releaseOrder(t, e, account.User.ID, order.ID); err != nil {
		t.Fatalf("关闭回收后应当仍可放行: %v", err)
	}
}

func TestAutoReleaseOnPaymentSkipsManualStep(t *testing.T) {
	e := newEnv(t, nil)
	if _, err := e.svc.Settings.Update(context.Background(), UpdateSettingsInput{
		Set: map[string]string{SettingAutoRelease: "true"},
	}); err != nil {
		t.Fatalf("打开到账即出纸: %v", err)
	}
	ctx := context.Background()

	d := newDeviceFixture(t, "fp-auto")
	account := register(t, e, d, "carol", "password123")
	printer, _ := e.svc.Printers.Upsert(ctx, UpsertPrinterInput{
		Code: "R04", Name: "测试机", CupsURI: "ipp://127.0.0.1:631/ipp/print",
		Status: "online", PriceSingleBWFen: 100, PaperSizes: []string{"A4"}, MaxCopies: 10,
	})
	file := uploadFile(t, e, account.User.ID)
	order := createOrder(t, e, account, printer.ID, file.ID, domain.DefaultPrintParams())

	result, err := reportPayment(t, e, account.User.ID, order.OrderNo, "evt-auto", order.AmountFen)
	if err != nil {
		t.Fatalf("上报失败: %v", err)
	}
	if !result.JobQueued {
		t.Fatal("自动放行模式下应已入队")
	}
	if result.CanRelease {
		t.Fatal("自动放行模式下不应再提示手动确认")
	}

	detail, _ := e.svc.Orders.Detail(ctx, account.User.ID, order.ID)
	if detail.Status != string(domain.OrderQueued) {
		t.Fatalf("状态 = %s，期望 queued", detail.Status)
	}
	if detail.JobStatus != string(domain.JobPending) {
		t.Fatalf("任务状态 = %s，期望 pending", detail.JobStatus)
	}
}

func TestDispatchedOrderMovesToPrinting(t *testing.T) {
	e, account, order, _ := setupAwaitingRelease(t, nil)
	ctx := context.Background()

	if _, err := releaseOrder(t, e, account.User.ID, order.ID); err != nil {
		t.Fatalf("确认打印失败: %v", err)
	}

	stored, err := e.svc.Q.GetOrderByID(ctx, order.ID)
	if err != nil {
		t.Fatalf("读取订单失败: %v", err)
	}
	if stored.Status != string(domain.OrderQueued) {
		t.Fatalf("状态 = %s，期望 queued", stored.Status)
	}

	due, err := e.svc.Q.ListDueJobs(ctx, store.ListDueJobsParams{
		NextRetryAt: e.now.Unix(),
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("查询待投递任务失败: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("应有 1 条待投递任务，实际 %d", len(due))
	}
}
