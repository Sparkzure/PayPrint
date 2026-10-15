package service

import (
	"testing"
	"time"

	"github.com/Sparkzure/PayPrint/server/internal/store"
)

func settingsEnv(t *testing.T) *paymentTestEnv {
	t.Helper()
	return newPaymentTestEnv(t)
}

func TestSettingsDefaultsAreBuiltIn(t *testing.T) {
	e := settingsEnv(t)

	rt := e.svc.Runtime()
	if rt.OrderTTL != 15*time.Minute || rt.ReleaseTTL != 30*time.Minute {
		t.Fatalf("内置默认值不正确：%+v", rt)
	}
	if !rt.OpenRegistration || rt.AutoReleaseOnPayment {
		t.Fatalf("内置默认值不正确：%+v", rt)
	}
	if rt.IPPMaxAttempts != 5 || rt.IPPTimeout != 20*time.Second {
		t.Fatalf("内置默认值不正确：%+v", rt)
	}
	if rt.MaxPendingOrdersPerUser != 0 {
		t.Fatalf("待支付上限默认应不限（0），实际 %d", rt.MaxPendingOrdersPerUser)
	}

	views, err := e.svc.Settings.List(e.ctx())
	if err != nil {
		t.Fatalf("列出配置: %v", err)
	}
	if len(views) != len(settingDefs) {
		t.Fatalf("应当列出全部可改项，实际 %d 项", len(views))
	}
	for _, view := range views {
		if view.Overridden {
			t.Fatalf("%s 不应被标记为已覆盖", view.Key)
		}
		if view.Value != view.Default {
			t.Fatalf("%s 未覆盖时 value(%s) 应等于 default(%s)", view.Key, view.Value, view.Default)
		}
	}
}

func TestSettingsOverrideTakesEffectAndReset(t *testing.T) {
	e := settingsEnv(t)

	if _, err := e.svc.Settings.Update(e.ctx(), UpdateSettingsInput{
		Set: map[string]string{
			SettingAutoRelease:      "true",
			SettingReleaseTTL:       "5m",
			SettingIPPMaxAttempts:   "9",
			SettingOpenRegistration: "false",
		},
		ActorID: "test-admin",
	}); err != nil {
		t.Fatalf("修改配置: %v", err)
	}

	rt := e.svc.Runtime()
	if !rt.AutoReleaseOnPayment || rt.ReleaseTTL != 5*time.Minute ||
		rt.IPPMaxAttempts != 9 || rt.OpenRegistration {
		t.Fatalf("修改后快照不正确：%+v", rt)
	}

	views, err := e.svc.Settings.List(e.ctx())
	if err != nil {
		t.Fatalf("列出配置: %v", err)
	}
	overridden := map[string]bool{}
	for _, view := range views {
		overridden[view.Key] = view.Overridden
	}
	if !overridden[SettingReleaseTTL] || overridden[SettingOrderTTL] {
		t.Fatalf("覆盖标记不正确：%+v", overridden)
	}

	if _, err := e.svc.Settings.Update(e.ctx(), UpdateSettingsInput{
		Reset:   []string{SettingReleaseTTL, SettingAutoRelease},
		ActorID: "test-admin",
	}); err != nil {
		t.Fatalf("还原配置: %v", err)
	}
	rt = e.svc.Runtime()
	if rt.ReleaseTTL != 30*time.Minute || rt.AutoReleaseOnPayment {
		t.Fatalf("还原后应回到配置默认值：%+v", rt)
	}
}

func TestSettingsRejectsInvalidValues(t *testing.T) {
	e := settingsEnv(t)

	cases := []struct {
		name  string
		set   map[string]string
		reset []string
	}{
		{"布尔值非 true/false", map[string]string{SettingAutoRelease: "yes"}, nil},
		{"时长缺单位", map[string]string{SettingOrderTTL: "15"}, nil},
		{"时长超出上限", map[string]string{SettingOrderTTL: "48h"}, nil},
		{"时长低于下限", map[string]string{SettingOrderTTL: "10s"}, nil},
		{"整数非数字", map[string]string{SettingIPPMaxAttempts: "many"}, nil},
		{"整数超出范围", map[string]string{SettingIPPMaxAttempts: "999"}, nil},
		{"未知键", map[string]string{"signature_skew": "1h"}, nil},
		{"还原未知键", nil, []string{"nonce_ttl"}},
		{"空改动", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.svc.Settings.Update(e.ctx(), UpdateSettingsInput{
				Set: tc.set, Reset: tc.reset, ActorID: "test-admin",
			})
			requireErrorCode(t, err, "validation_error")
		})
	}

	rows, err := e.svc.Q.ListSettings(e.ctx())
	if err != nil {
		t.Fatalf("读取配置行: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("被拒绝的修改不应落库，实际留下 %d 行", len(rows))
	}

	_, err = e.svc.Settings.Update(e.ctx(), UpdateSettingsInput{
		Set: map[string]string{
			SettingReleaseTTL: "5m",
			SettingOrderTTL:   "绝不是时长",
		},
		ActorID: "test-admin",
	})
	requireErrorCode(t, err, "validation_error")
	if e.svc.Runtime().ReleaseTTL != 30*time.Minute {
		t.Fatal("整组被拒时，合法的那一项也不应生效")
	}
}

func TestSettingsToleratesUnknownAndCorruptRows(t *testing.T) {
	e := settingsEnv(t)
	ctx := e.ctx()

	if err := e.svc.Q.UpsertSetting(ctx, mustSetting(e, "future_setting", "whatever")); err != nil {
		t.Fatalf("写入未知键: %v", err)
	}
	if err := e.svc.Q.UpsertSetting(ctx, mustSetting(e, SettingOrderTTL, "半小时")); err != nil {
		t.Fatalf("写入非法值: %v", err)
	}

	if err := e.svc.Settings.Reload(ctx); err != nil {
		t.Fatalf("遇到脏数据也必须能装载: %v", err)
	}

	rt := e.svc.Runtime()
	if rt.OrderTTL != 15*time.Minute {
		t.Fatalf("非法值应回退到默认，实际 %s", rt.OrderTTL)
	}

	if _, err := e.svc.Settings.Update(ctx, UpdateSettingsInput{
		Set: map[string]string{SettingReleaseTTL: "10m"}, ActorID: "test-admin",
	}); err != nil {
		t.Fatalf("脏数据不应影响后续修改: %v", err)
	}
	if e.svc.Runtime().ReleaseTTL != 10*time.Minute {
		t.Fatal("修改未生效")
	}
}

func TestBootstrapSeedsUnverifiedSiteWithoutProber(t *testing.T) {
	e := settingsEnv(t)

	if e.svc.Prober != nil {
		t.Fatal("本用例要求没有探测能力")
	}

	e.mustExec(`DELETE FROM printers`)

	if err := e.svc.Printers.Bootstrap(e.ctx()); err != nil {
		t.Fatalf("CUPS 未就绪时 Bootstrap 也必须成功: %v", err)
	}

	rows, err := e.svc.Q.ListAllPrinters(e.ctx())
	if err != nil {
		t.Fatalf("读取打印机: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("应当种下一台默认打印机，实际 %d 台", len(rows))
	}
	if rows[0].Status != "offline" {
		t.Fatalf("没联系过 CUPS 的打印机不能声称在线，实际状态 %q", rows[0].Status)
	}

	if err := e.svc.Q.SetPrinterStatus(e.ctx(), mustStatus(e, rows[0].ID, "online")); err != nil {
		t.Fatalf("上线打印机: %v", err)
	}
	if err := e.svc.Printers.Bootstrap(e.ctx()); err != nil {
		t.Fatalf("第二次 Bootstrap: %v", err)
	}
	after, err := e.svc.Q.ListAllPrinters(e.ctx())
	if err != nil {
		t.Fatalf("读取打印机: %v", err)
	}
	if len(after) != 1 || after[0].Status != "online" {
		t.Fatal("已有打印机时 Bootstrap 不应改动它")
	}
}

func mustSetting(e *paymentTestEnv, key, value string) store.UpsertSettingParams {
	return store.UpsertSettingParams{Key: key, Value: value, UpdatedAt: e.now.Unix()}
}

func mustStatus(e *paymentTestEnv, id, status string) store.SetPrinterStatusParams {
	return store.SetPrinterStatusParams{Status: status, UpdatedAt: e.now.Unix(), ID: id}
}
