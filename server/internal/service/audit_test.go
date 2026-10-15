package service

import (
	"testing"
	"time"

	"github.com/Sparkzure/PayPrint/server/internal/domain"
)

func TestOrderAuditIsWrittenOnCreate(t *testing.T) {
	e := newPaymentTestEnv(t)

	e.mustExec(`UPDATE printers SET supports_color = 1, supports_duplex = 1 WHERE id = ?`, e.siteA)

	userID := e.createUser("audit-user", domain.RoleUser)
	fileID := e.createFile("file-audit", userID)

	view, err := e.svc.Orders.Create(e.ctx(), CreateOrderInput{
		UserID:            userID,
		DeviceLabel:       "打印点的机器",
		DeviceFingerprint: "fp-audit",
		FileID:            fileID,
		PrinterID:         e.siteA,
		Params: domain.PrintParams{
			Copies:    2,
			Duplex:    domain.DuplexLongEdge,
			ColorMode: domain.ColorModeColor,
		},
	})
	if err != nil {
		t.Fatalf("下单失败: %v", err)
	}

	rows, total, err := e.svc.Audits.List(e.ctx(), 10, 0)
	if err != nil {
		t.Fatalf("读取审计日志失败: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("一条订单应当对应一条审计，实际 total=%d len=%d", total, len(rows))
	}

	got := rows[0]

	if got.UserID != userID || got.Username != "audit-user" {
		t.Fatalf("账号字段不正确: user_id=%q username=%q", got.UserID, got.Username)
	}

	if got.DeviceLabel != "打印点的机器" || got.DeviceFingerprint != "fp-audit" {
		t.Fatalf("设备快照不正确: label=%q fingerprint=%q", got.DeviceLabel, got.DeviceFingerprint)
	}

	if got.OrderNo != view.OrderNo || got.OrderID != view.ID || got.AmountFen != view.AmountFen {
		t.Fatalf("订单字段不一致: %+v（订单 %s / %d 分）", got, view.OrderNo, view.AmountFen)
	}
	if got.AmountText != domain.FormatYuan(view.AmountFen) {
		t.Fatalf("金额文案 = %q，期望 %q", got.AmountText, domain.FormatYuan(view.AmountFen))
	}

	if got.FileName != "doc.pdf" {
		t.Fatalf("打印文件名 = %q，期望 doc.pdf", got.FileName)
	}
	if got.ColorText != "彩色" || got.DuplexText != "双面（长边）" {
		t.Fatalf("打印类型文案不正确: %q / %q", got.ColorText, got.DuplexText)
	}
	if got.Copies != 2 {
		t.Fatalf("打印份数 = %d，期望 2", got.Copies)
	}

	if got.TotalPages != 2 {
		t.Fatalf("打印总页数 = %d，期望 2", got.TotalPages)
	}
}

func TestOrderAuditSurvivesDeviceRelease(t *testing.T) {
	e := newPaymentTestEnv(t)
	userID := e.createUser("audit-owner", domain.RoleUser)
	dev := e.createDevice("dev-audit-owner", "fp-audit-owner", userID)
	fileID := e.createFile("file-audit-owner", userID)

	if _, err := e.svc.Orders.Create(e.ctx(), CreateOrderInput{
		UserID:            userID,
		DeviceLabel:       "我的设备",
		DeviceFingerprint: dev.Fingerprint,
		FileID:            fileID,
		PrinterID:         e.siteA,
		Params:            domain.DefaultPrintParams(),
	}); err != nil {
		t.Fatalf("下单失败: %v", err)
	}

	rows, _, err := e.svc.Audits.List(e.ctx(), 10, 0)
	if err != nil {
		t.Fatalf("读取审计日志失败: %v", err)
	}
	before := rows[0]

	if err := e.svc.Devices.AdminReleaseDevice(e.ctx(), ReleaseDeviceInput{
		DeviceID: dev.ID, Reason: "测试：设备转让", ActorID: "admin-1",
	}); err != nil {
		t.Fatalf("解绑设备失败: %v", err)
	}

	released := e.device(dev.ID)
	if released.Fingerprint == dev.Fingerprint {
		t.Fatal("前置条件不成立：解绑应当让出指纹")
	}

	after, _, err := e.svc.Audits.List(e.ctx(), 10, 0)
	if err != nil {
		t.Fatalf("再次读取审计日志失败: %v", err)
	}
	if after[0].DeviceFingerprint != before.DeviceFingerprint ||
		after[0].DeviceLabel != before.DeviceLabel {
		t.Fatalf("解绑不应改动审计里的设备快照: before=%q/%q after=%q/%q",
			before.DeviceLabel, before.DeviceFingerprint,
			after[0].DeviceLabel, after[0].DeviceFingerprint)
	}
	if after[0].OrderNo != before.OrderNo || after[0].AmountFen != before.AmountFen {
		t.Fatal("解绑不应改动审计里的订单字段")
	}
}

func TestOrderAuditListsNewestFirst(t *testing.T) {
	e := newPaymentTestEnv(t)
	userID := e.createUser("audit-order", domain.RoleUser)
	fileID := e.createFile("file-audit-order", userID)

	var lastNo string

	for copies := 1; copies <= 3; copies++ {
		view, err := e.svc.Orders.Create(e.ctx(), CreateOrderInput{
			UserID:    userID,
			FileID:    fileID,
			PrinterID: e.siteA,
			Params:    domain.PrintParams{Copies: copies},
		})
		if err != nil {
			t.Fatalf("第 %d 次下单失败: %v", copies, err)
		}
		lastNo = view.OrderNo

		e.now = e.now.Add(time.Second)
	}

	rows, total, err := e.svc.Audits.List(e.ctx(), 10, 0)
	if err != nil {
		t.Fatalf("读取审计日志失败: %v", err)
	}
	if total != 3 {
		t.Fatalf("总数 = %d，期望 3", total)
	}
	if rows[0].OrderNo != lastNo {
		t.Fatalf("最新一条应当排在最前，实际 %s（期望 %s）", rows[0].OrderNo, lastNo)
	}
}
