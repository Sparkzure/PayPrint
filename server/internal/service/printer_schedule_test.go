package service

import (
	"context"
	"testing"
	"time"

	"github.com/Sparkzure/PayPrint/server/internal/domain"
	"github.com/Sparkzure/PayPrint/server/internal/store"
)

func TestParseOnlineWindowsTolerantOnBadData(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want int
	}{
		{"空值 = 全天在线", "", 0},
		{"空数组 = 全天在线", "[]", 0},
		{"数据损坏 = 全天在线", "{oops", 0},
		{"日期乱填的条目被丢弃", `[{"days":[0,9],"start":"08:00","end":"12:00"}]`, 0},
		{"时间格式不对的条目被丢弃", `[{"days":[1],"start":"8点","end":"12:00"}]`, 0},
		{"开始等于结束的条目被丢弃", `[{"days":[1],"start":"08:00","end":"08:00"}]`, 0},
		{"混合数据只保留合法条目", `[{"days":[3,1,1],"start":"08:00","end":"12:00"},{"days":[],"start":"x","end":"y"}]`, 1},
	}
	for _, tc := range cases {
		got := ParseOnlineWindows(tc.raw)
		if len(got) != tc.want {
			t.Fatalf("%s：解析出 %d 条，期望 %d 条（%+v）", tc.name, len(got), tc.want, got)
		}
	}

	got := ParseOnlineWindows(`[{"days":[5,1,1,3],"start":"8:00","end":"12:00"}]`)
	if len(got) != 1 {
		t.Fatalf("期望 1 条，实际 %d 条", len(got))
	}
	if want := []int{1, 3, 5}; len(got[0].Days) != 3 ||
		got[0].Days[0] != want[0] || got[0].Days[1] != want[1] || got[0].Days[2] != want[2] {
		t.Fatalf("工作日未去重排序：%+v", got[0].Days)
	}
}

func TestInOnlineWindow(t *testing.T) {

	friday := func(hour, minute int) time.Time {
		return time.Date(2026, 10, 9, hour, minute, 0, 0, time.UTC)
	}
	saturday := func(hour, minute int) time.Time {
		return time.Date(2026, 10, 10, hour, minute, 0, 0, time.UTC)
	}

	workdays := []OnlineWindow{{Days: []int{1, 2, 3, 4, 5}, Start: "08:00", End: "22:00"}}
	overnight := []OnlineWindow{{Days: []int{5}, Start: "22:00", End: "02:00"}}

	cases := []struct {
		name string
		wins []OnlineWindow
		at   time.Time
		want bool
	}{
		{"没配时段时任意时刻都在线", nil, friday(3, 0), true},
		{"时段内", workdays, friday(12, 0), true},
		{"开始时刻算在内", workdays, friday(8, 0), true},
		{"结束时刻不算（含左不含右）", workdays, friday(22, 0), false},
		{"开始前一分钟", workdays, friday(7, 59), false},
		{"工作日之外", workdays, saturday(12, 0), false},
		{"跨午夜：开始日的深夜", overnight, friday(23, 30), true},
		{"跨午夜：次日的凌晨", overnight, saturday(1, 0), true},
		{"跨午夜：次日结束时刻不算", overnight, saturday(2, 0), false},
		{"跨午夜：不做日期回绕，周日的凌晨不算", overnight, saturday(23, 30), false},
	}
	for _, tc := range cases {
		if got := InOnlineWindow(tc.wins, tc.at); got != tc.want {
			t.Fatalf("%s：%s 判定为 %v，期望 %v", tc.name, tc.at.Format("2006-01-02 15:04"), got, tc.want)
		}
	}
}

func TestDescribeOnlineWindows(t *testing.T) {
	cases := []struct {
		wins []OnlineWindow
		want string
	}{
		{nil, "全天在线"},
		{[]OnlineWindow{{Days: []int{1, 2, 3, 4, 5}, Start: "08:00", End: "22:00"}},
			"周一至周五 08:00-22:00"},
		{[]OnlineWindow{{Days: []int{7, 6, 5, 4, 3, 2, 1}, Start: "00:00", End: "23:59"}},
			"每天 00:00-23:59"},
		{[]OnlineWindow{{Days: []int{6, 7}, Start: "09:00", End: "12:00"}},
			"周六、周日 09:00-12:00"},
		{[]OnlineWindow{{Days: []int{1, 3, 5}, Start: "09:00", End: "12:00"}},
			"周一、周三、周五 09:00-12:00"},
		{
			[]OnlineWindow{
				{Days: []int{1, 2, 3, 4, 5}, Start: "08:00", End: "12:00"},
				{Days: []int{6}, Start: "22:00", End: "02:00"},
			},
			"周一至周五 08:00-12:00、周六 22:00-02:00",
		},
	}
	for i, tc := range cases {
		if got := DescribeOnlineWindows(tc.wins); got != tc.want {
			t.Fatalf("用例 %d：文案 = %q，期望 %q", i, got, tc.want)
		}
	}
}

func TestNextOnlineStart(t *testing.T) {
	workdays := []OnlineWindow{{Days: []int{1, 2, 3, 4, 5}, Start: "08:00", End: "22:00"}}

	open := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	if got := NextOnlineStart(workdays, open); !got.Equal(open) {
		t.Fatalf("时段内应原样返回，实际 %s", got)
	}

	got := NextOnlineStart(workdays, time.Date(2026, 10, 9, 7, 0, 0, 0, time.UTC))
	if want := time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("下一次开放 = %s，期望 %s", got, want)
	}

	got = NextOnlineStart(workdays, time.Date(2026, 10, 9, 23, 0, 0, 0, time.UTC))
	if want := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("下一次开放 = %s，期望 %s", got, want)
	}

	overnight := []OnlineWindow{{Days: []int{5}, Start: "22:00", End: "02:00"}}
	got = NextOnlineStart(overnight, time.Date(2026, 10, 10, 3, 0, 0, 0, time.UTC))
	if want := time.Date(2026, 10, 16, 22, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("跨午夜时段的下一次开放 = %s，期望 %s", got, want)
	}

	at := time.Date(2026, 10, 10, 3, 0, 0, 0, time.UTC)
	if got := NextOnlineStart(nil, at); !got.Equal(at) {
		t.Fatalf("全天在线应原样返回，实际 %s", got)
	}
}

func TestValidateOnlineWindows(t *testing.T) {
	bad := []struct {
		name string
		wins []OnlineWindow
	}{
		{"时间格式不对", []OnlineWindow{{Days: []int{1}, Start: "8点", End: "12:00"}}},
		{"结束时间格式不对", []OnlineWindow{{Days: []int{1}, Start: "08:00", End: "24:00"}}},
		{"开始等于结束", []OnlineWindow{{Days: []int{1}, Start: "08:00", End: "08:00"}}},
		{"没选工作日", []OnlineWindow{{Days: nil, Start: "08:00", End: "12:00"}}},
		{"工作日超范围", []OnlineWindow{{Days: []int{0}, Start: "08:00", End: "12:00"}}},
		{"条数超上限", func() []OnlineWindow {
			out := make([]OnlineWindow, 0, maxOnlineWindows+1)
			for i := 0; i <= maxOnlineWindows; i++ {
				out = append(out, OnlineWindow{Days: []int{1}, Start: "08:00", End: "12:00"})
			}
			return out
		}()},
	}
	for _, tc := range bad {
		if err := ValidateOnlineWindows(tc.wins); err == nil {
			t.Fatalf("%s：应当被拒绝", tc.name)
		}
	}

	ok := []OnlineWindow{
		{Days: []int{1, 2, 3, 4, 5}, Start: "08:00", End: "12:00"},
		{Days: []int{5, 6}, Start: "22:00", End: "02:00"},
	}
	if err := ValidateOnlineWindows(ok); err != nil {
		t.Fatalf("合法时段被拒: %v", err)
	}
	if err := ValidateOnlineWindows(nil); err != nil {
		t.Fatalf("空时段（全天在线）被拒: %v", err)
	}
}

func TestCreateOrderRespectsOnlineWindow(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()

	account := register(t, e, newDeviceFixture(t, "fp-window"), "frank", "password123")
	printer, err := e.svc.Printers.Upsert(ctx, UpsertPrinterInput{
		Code: "W01", Name: "时段机", CupsURI: "ipp://127.0.0.1:631/ipp/print",
		Status: "online", PriceSingleBWFen: 100, PaperSizes: []string{"A4"}, MaxCopies: 10,

		OnlineWindows: []OnlineWindow{{Days: []int{1, 2, 3, 4, 5}, Start: "08:00", End: "12:10"}},
	})
	if err != nil {
		t.Fatalf("登记打印机: %v", err)
	}
	file := uploadFile(t, e, account.User.ID)

	order := createOrder(t, e, account, printer.ID, file.ID, domain.DefaultPrintParams())

	view := printerView(t, e, "W01")
	if !view.WithinSchedule {
		t.Fatal("营业时段内 within_schedule 应为 true")
	}
	if view.OnlineWindowsText != "周一至周五 08:00-12:10" {
		t.Fatalf("时段文案 = %q", view.OnlineWindowsText)
	}

	e.advance(15 * time.Minute)
	_, err = createOrderErr(t, e, account, printer.ID, file.ID, domain.DefaultPrintParams())
	assertCode(t, err, "printer_closed")

	if printerView(t, e, "W01").WithinSchedule {
		t.Fatal("关门后 within_schedule 应为 false")
	}

	detail, err := e.svc.Orders.Detail(ctx, account.User.ID, order.ID)
	if err != nil {
		t.Fatalf("查询订单: %v", err)
	}
	if detail.Status != string(domain.OrderAwaitingPayment) {
		t.Fatalf("已有订单状态 = %s，期望 awaiting_payment", detail.Status)
	}
}

func TestReleaseBlockedOutsideOnlineWindow(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()

	d := newDeviceFixture(t, "fp-release-window")
	account := register(t, e, d, "grace", "password123")
	printer, err := e.svc.Printers.Upsert(ctx, UpsertPrinterInput{
		Code: "W02", Name: "打烊机", CupsURI: "ipp://127.0.0.1:631/ipp/print",
		Status: "online", PriceSingleBWFen: 100, PaperSizes: []string{"A4"}, MaxCopies: 10,

		OnlineWindows: []OnlineWindow{{Days: []int{1, 2, 3, 4, 5}, Start: "08:00", End: "12:10"}},
	})
	if err != nil {
		t.Fatalf("登记打印机: %v", err)
	}
	file := uploadFile(t, e, account.User.ID)
	order := createOrder(t, e, account, printer.ID, file.ID, domain.DefaultPrintParams())
	if _, err := reportPayment(t, e, account.User.ID, order.OrderNo, "evt-"+order.OrderNo, order.AmountFen); err != nil {
		t.Fatalf("支付上报失败: %v", err)
	}

	e.advance(15 * time.Minute)
	_, err = releaseOrder(t, e, account.User.ID, order.ID)
	assertCode(t, err, "printer_closed")

	if _, err := e.svc.Q.GetJobByOrderID(ctx, order.ID); err == nil {
		t.Fatal("时段外放行不应创建打印任务")
	}
	detail, err := e.svc.Orders.Detail(ctx, account.User.ID, order.ID)
	if err != nil {
		t.Fatalf("查询订单: %v", err)
	}
	if detail.Status != string(domain.OrderAwaitingRelease) {
		t.Fatalf("状态 = %s，期望仍是 awaiting_release", detail.Status)
	}

	reopened := e.svc.Printers.NextOpenAt(store.Printer{OnlineWindows: `[{"days":[1,2,3,4,5],"start":"08:00","end":"12:10"}]`},
		time.Date(2026, 10, 10, 3, 0, 0, 0, time.UTC))
	if want := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC); !reopened.Equal(want) {
		t.Fatalf("下一次开放 = %s，期望 %s", reopened, want)
	}
}

func printerView(t *testing.T, e *env, code string) PrinterView {
	t.Helper()
	views, err := e.svc.Printers.List(context.Background(), false)
	if err != nil {
		t.Fatalf("列出打印机: %v", err)
	}
	for _, v := range views {
		if v.Code == code {
			return v
		}
	}
	t.Fatalf("找不到打印机 %s", code)
	return PrinterView{}
}

func (e *paymentTestEnv) setOnlineWindows(printerID string, wins []OnlineWindow) {
	e.t.Helper()
	encoded, err := EncodeOnlineWindows(wins)
	if err != nil {
		e.t.Fatalf("编码时段: %v", err)
	}
	e.mustExec(`UPDATE printers SET online_windows = ? WHERE id = ?`, encoded, printerID)
}

func TestDispatchDefersJobOutsideOnlineWindow(t *testing.T) {
	e := newPaymentTestEnv(t)
	ctx := e.ctx()

	e.setOnlineWindows(e.siteA, []OnlineWindow{
		{Days: []int{1, 2, 3, 4, 5}, Start: "13:00", End: "18:00"},
	})
	order, _ := e.seedUserOrder("defer", e.siteA, 60, domain.OrderQueued,
		e.now.Unix(), e.now.Add(10*time.Minute).Unix())
	e.createJob("job-defer", order.ID, domain.JobPending, 0, 5, 0)

	stats, err := e.svc.Jobs.DispatchDue(ctx, 10)
	if err != nil {
		t.Fatalf("派发失败: %v", err)
	}
	if stats.Deferred != 1 || stats.Submitted != 0 || stats.Failed != 0 {
		t.Fatalf("派发统计 = %+v，期望顺延 1 条且不投递不判死", stats)
	}

	job, err := e.svc.Q.GetJobByID(ctx, "job-defer")
	if err != nil {
		t.Fatalf("读取任务: %v", err)
	}
	if job.Status != string(domain.JobPending) {
		t.Fatalf("任务状态 = %s，期望 pending", job.Status)
	}
	if job.Attempts != 0 {
		t.Fatalf("顺延不应消耗重试预算，attempts = %d", job.Attempts)
	}
	if want := time.Date(2026, 10, 9, 13, 0, 0, 0, time.UTC).Unix(); job.NextRetryAt != want {
		t.Fatalf("下一次投递时间 = %d，期望 %d（当天 13:00 开门）", job.NextRetryAt, want)
	}
	if got := e.order(order.ID).Status; got != string(domain.OrderQueued) {
		t.Fatalf("订单状态 = %s，期望仍为 queued（纸迟早要出）", got)
	}

	again, err := e.svc.Jobs.DispatchDue(ctx, 10)
	if err != nil {
		t.Fatalf("再次派发失败: %v", err)
	}
	if again.Claimed != 0 {
		t.Fatalf("顺延后的任务不应再次到期，实际认领 %d 条", again.Claimed)
	}
}

func TestUnsetScheduleMeansAlwaysOnline(t *testing.T) {
	e := newPaymentTestEnv(t)

	row, err := e.svc.Q.GetPrinterByID(e.ctx(), e.siteA)
	if err != nil {
		t.Fatalf("读取打印机: %v", err)
	}
	if row.OnlineWindows != "[]" {
		t.Fatalf("未配置时段的打印机应存 '[]'，实际 %q", row.OnlineWindows)
	}

	at := time.Date(2026, 10, 10, 3, 0, 0, 0, time.UTC)
	if !InOnlineWindow(ParseOnlineWindows(row.OnlineWindows), at) {
		t.Fatal("未配置时段应视为全天在线")
	}
	if got := e.svc.Printers.NextOpenAt(row, at); !got.Equal(at) {
		t.Fatalf("全天在线时下一次开放时刻应就是当前时间，实际 %s", got)
	}
	if got := InOnlineWindow(ParseOnlineWindows(row.OnlineWindows), at); !got {
		t.Fatal("全天在线时任意时刻都应判定为在线")
	}
}

func TestUpsertPrinterValidatesOnlineWindows(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()

	if _, err := e.svc.Printers.Upsert(ctx, UpsertPrinterInput{
		Code: "W03", Name: "非法时段机", CupsURI: "ipp://127.0.0.1:631/ipp/print",
		OnlineWindows: []OnlineWindow{{Days: []int{1}, Start: "22:00", End: "22:00"}},
	}); err == nil {
		t.Fatal("开始与结束相同的时段必须被拒绝")
	}

	view, err := e.svc.Printers.Upsert(ctx, UpsertPrinterInput{
		Code: "W03", Name: "夜班机", CupsURI: "ipp://127.0.0.1:631/ipp/print",
		Status: "online", PriceSingleBWFen: 100, PaperSizes: []string{"A4"}, MaxCopies: 10,
		OnlineWindows: []OnlineWindow{{Days: []int{5, 6}, Start: "22:00", End: "02:00"}},
	})
	if err != nil {
		t.Fatalf("保存时段: %v", err)
	}
	if view.OnlineWindowsText != "周五、周六 22:00-02:00" {
		t.Fatalf("回显文案 = %q", view.OnlineWindowsText)
	}

	view, err = e.svc.Printers.Upsert(ctx, UpsertPrinterInput{
		Code: "W03", Name: "夜班机", CupsURI: "ipp://127.0.0.1:631/ipp/print",
		Status: "online", PriceSingleBWFen: 100, PaperSizes: []string{"A4"}, MaxCopies: 10,
	})
	if err != nil {
		t.Fatalf("清空时段: %v", err)
	}
	if len(view.OnlineWindows) != 0 || !view.WithinSchedule {
		t.Fatalf("清空后应回到全天在线：%+v", view)
	}
	row, err := e.svc.Q.GetPrinterByCode(ctx, "W03")
	if err != nil {
		t.Fatalf("读取打印机: %v", err)
	}
	if row.OnlineWindows != "[]" {
		t.Fatalf("清空后库里的值 = %q，期望 []", row.OnlineWindows)
	}
}
