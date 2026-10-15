package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Sparkzure/PayPrint/server/internal/domain"
	"github.com/Sparkzure/PayPrint/server/internal/store"
)

type RuntimeSettings struct {
	AutoReleaseOnPayment       bool
	RequirePaymentVerification bool
	ReleaseTTL                 time.Duration
	OrderTTL                   time.Duration
	MaxPendingOrdersPerUser    int
	OpenRegistration           bool
	IPPTimeout                 time.Duration
	IPPMaxAttempts             int
	IPPPollInterval            time.Duration

	StuckJobTTL time.Duration
}

const (
	SettingAutoRelease      = "auto_release_on_payment"
	SettingPaymentVerify    = "require_payment_verification"
	SettingReleaseTTL       = "release_ttl"
	SettingOrderTTL         = "order_ttl"
	SettingMaxPendingOrders = "max_pending_orders_per_user"
	SettingOpenRegistration = "open_registration"
	SettingIPPTimeout       = "ipp_timeout"
	SettingIPPMaxAttempts   = "ipp_max_attempts"
	SettingIPPPollInterval  = "ipp_poll_interval"
	SettingStuckJobTTL      = "stuck_job_ttl"
)

type settingKind string

const (
	kindBool     settingKind = "bool"
	kindDuration settingKind = "duration"
	kindInt      settingKind = "int"
)

type SettingDef struct {
	Key   string
	Label string
	Help  string
	Group string
	Kind  settingKind

	Min int64
	Max int64

	Default string
}

var settingDefs = []SettingDef{
	{
		Key: SettingAutoRelease, Label: "到账即出纸", Group: "打印放行", Kind: kindBool,
		Default: "false",
		Help:    "打开后，到账通过校验链就直接投递打印，不再等用户点「确认打印」。代价是出的纸可能被下一个人顺手拿走。",
	},
	{
		Key: SettingPaymentVerify, Label: "放行前强制官方核验", Group: "打印放行", Kind: kindBool,
		Default: "false",
		Help:    "打开后，确认打印前必须拿到官方查单的明确「已支付」结论，否则拒绝（用户可重试）。需要先配好微信商户信息，否则订单会一直卡住。",
	},
	{
		Key: SettingReleaseTTL, Label: "确认打印时限", Group: "打印放行", Kind: kindDuration,
		Min: 0, Max: int64((24 * time.Hour).Seconds()),
		Default: "30m",
		Help:    "「待确认打印」的存活时间。超时订单转为待退款并打告警日志，避免钱挂在账上而纸不出来。填 0 表示不回收（责任全交给人工）。",
	},
	{
		Key: SettingOrderTTL, Label: "待支付时长", Group: "订单", Kind: kindDuration,
		Min: int64(time.Minute.Seconds()), Max: int64((24 * time.Hour).Seconds()),
		Default: "15m",
		Help:    "下单后多久未收到到账就自动关闭。太短会出现「用户还在扫码、订单已经没了」，太长会占着同金额匹配的名额。",
	},
	{
		Key: SettingMaxPendingOrders, Label: "单人待支付订单上限", Group: "订单", Kind: kindInt,
		Min: 0, Max: 100,
		Default: "0",
		Help:    "一个账号同时最多能有几笔「待支付」订单，超出后下单被拒（0 表示不限）。上限能防止同金额订单互相冲突导致到账判歧义，也防刷单占名额。",
	},
	{
		Key: SettingOpenRegistration, Label: "开放自助注册", Group: "账号", Kind: kindBool,
		Default: "true",
		Help:    "关闭后只能由管理员在面板里建号。注意：空库后的第一个自助注册账号会自动成为管理员（bootstrap），所以首次部署后应立即注册管理员账号，再按需关闭注册。",
	},
	{
		Key: SettingIPPTimeout, Label: "IPP 请求超时", Group: "打印投递", Kind: kindDuration,
		Min: 1, Max: int64((5 * time.Minute).Seconds()),
		Default: "20s",
		Help:    "单次 IPP 请求的超时。打印机慢或网络差时可以调大。",
	},
	{
		Key: SettingIPPMaxAttempts, Label: "投递最大重试次数", Group: "打印投递", Kind: kindInt,
		Min: 1, Max: 20,
		Default: "5",
		Help:    "投递失败后的重试上限（指数退避）。只影响新建的任务，已经建好的任务按它自己的次数走。",
	},
	{
		Key: SettingIPPPollInterval, Label: "作业状态回查间隔", Group: "打印投递", Kind: kindDuration,
		Min: 2, Max: int64((10 * time.Minute).Seconds()),
		Default: "15s",
		Help:    "回查在途作业状态的间隔（Print-Job 成功只代表入队，不代表已出纸）。调小会更及时，代价是多探测几次打印机。",
	},
	{
		Key: SettingStuckJobTTL, Label: "在途任务超时转人工", Group: "打印投递", Kind: kindDuration,
		Min: 0, Max: int64((24 * time.Hour).Seconds()),
		Default: "1h",
		Help: "任务停在「已入队 / 打印中 / 打印中断」超过这个时长、且打印机不再报告任何进展时，" +
			"转「待人工处理」、订单转「待退款」并打告警。卡纸没人清、打印机断电或掉网都属于这种情况。" +
			"必须大于最慢的一次打印耗时，否则正常的长任务会被判成卡住。填 0 表示不回收。",
	},
}

func settingDefOf(key string) (SettingDef, bool) {
	for _, def := range settingDefs {
		if def.Key == key {
			return def, true
		}
	}
	return SettingDef{}, false
}

type SettingView struct {
	Key        string `json:"key"`
	Label      string `json:"label"`
	Help       string `json:"help"`
	Group      string `json:"group"`
	Kind       string `json:"kind"`
	Value      string `json:"value"`
	Default    string `json:"default"`
	Overridden bool   `json:"overridden"`
	Min        int64  `json:"min,omitempty"`
	Max        int64  `json:"max,omitempty"`
}

type UpdateSettingsInput struct {
	Set     map[string]string
	Reset   []string
	ActorID string
	IP      string
}

type SettingsService struct{ *Services }

type settingsStore struct {
	ptr atomic.Pointer[RuntimeSettings]
}

func (s *Services) Runtime() RuntimeSettings {
	if v := s.settings.ptr.Load(); v != nil {
		return *v
	}

	return RuntimeSettings{}
}

func (svc *SettingsService) Reload(ctx context.Context) error {
	rows, err := svc.Q.ListSettings(ctx)
	if err != nil {
		return domain.ErrInternal.WithErr(err)
	}
	overrides := make(map[string]string, len(rows))
	for _, row := range rows {
		overrides[row.Key] = row.Value
	}

	rt := defaultRuntime()
	for key, raw := range overrides {
		def, ok := settingDefOf(key)
		if !ok {

			svc.Log.WarnContext(ctx, "忽略未知的运行时配置项", "key", key)
			continue
		}
		if err := applySetting(def, raw, &rt); err != nil {

			svc.Log.ErrorContext(ctx, "运行时配置项非法，已回退到默认值",
				"key", key, "value", raw, "error", err)
		}
	}
	svc.settings.ptr.Store(&rt)

	if svc.IPP != nil {
		svc.IPP.SetTimeout(rt.IPPTimeout)
	}
	return nil
}

func defaultRuntime() RuntimeSettings {
	rt := RuntimeSettings{}
	for _, def := range settingDefs {
		if err := applySetting(def, def.Default, &rt); err != nil {

			panic(fmt.Sprintf("内置默认值非法: %s = %s: %v", def.Key, def.Default, err))
		}
	}
	return rt
}

func applySetting(def SettingDef, raw string, rt *RuntimeSettings) error {
	value := strings.TrimSpace(raw)
	switch def.Kind {
	case kindBool:
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("%s 只能是 true 或 false", def.Label)
		}
		switch def.Key {
		case SettingAutoRelease:
			rt.AutoReleaseOnPayment = parsed
		case SettingPaymentVerify:
			rt.RequirePaymentVerification = parsed
		case SettingOpenRegistration:
			rt.OpenRegistration = parsed
		}
	case kindInt:
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%s 必须是整数", def.Label)
		}
		if int64(parsed) < def.Min || int64(parsed) > def.Max {
			return fmt.Errorf("%s 必须在 %d 到 %d 之间", def.Label, def.Min, def.Max)
		}
		if def.Key == SettingIPPMaxAttempts {
			rt.IPPMaxAttempts = parsed
		}
		if def.Key == SettingMaxPendingOrders {
			rt.MaxPendingOrdersPerUser = parsed
		}
	case kindDuration:
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("%s 必须是时长（例如 30m、15s）", def.Label)
		}
		seconds := int64(parsed.Seconds())
		if seconds < def.Min || seconds > def.Max {
			return fmt.Errorf("%s 必须在 %s 到 %s 之间",
				def.Label, time.Duration(def.Min)*time.Second, time.Duration(def.Max)*time.Second)
		}
		switch def.Key {
		case SettingReleaseTTL:
			rt.ReleaseTTL = parsed
		case SettingOrderTTL:
			rt.OrderTTL = parsed
		case SettingIPPTimeout:
			rt.IPPTimeout = parsed
		case SettingIPPPollInterval:
			rt.IPPPollInterval = parsed
		case SettingStuckJobTTL:
			rt.StuckJobTTL = parsed
		}
	}
	return nil
}

func settingsOf(rt RuntimeSettings) map[string]string {
	return map[string]string{
		SettingAutoRelease:      strconv.FormatBool(rt.AutoReleaseOnPayment),
		SettingPaymentVerify:    strconv.FormatBool(rt.RequirePaymentVerification),
		SettingReleaseTTL:       rt.ReleaseTTL.String(),
		SettingOrderTTL:         rt.OrderTTL.String(),
		SettingMaxPendingOrders: strconv.Itoa(rt.MaxPendingOrdersPerUser),
		SettingOpenRegistration: strconv.FormatBool(rt.OpenRegistration),
		SettingIPPTimeout:       rt.IPPTimeout.String(),
		SettingIPPMaxAttempts:   strconv.Itoa(rt.IPPMaxAttempts),
		SettingIPPPollInterval:  rt.IPPPollInterval.String(),
		SettingStuckJobTTL:      rt.StuckJobTTL.String(),
	}
}

func (svc *SettingsService) List(ctx context.Context) ([]SettingView, error) {
	rows, err := svc.Q.ListSettings(ctx)
	if err != nil {
		return nil, domain.ErrInternal.WithErr(err)
	}
	overridden := make(map[string]bool, len(rows))
	stored := make(map[string]string, len(rows))
	for _, row := range rows {
		overridden[row.Key] = true
		stored[row.Key] = row.Value
	}

	defaults := settingsOf(defaultRuntime())
	current := settingsOf(svc.Runtime())

	views := make([]SettingView, 0, len(settingDefs))
	for _, def := range settingDefs {
		value := current[def.Key]
		if overridden[def.Key] {

			value = stored[def.Key]
		}
		views = append(views, SettingView{
			Key:        def.Key,
			Label:      def.Label,
			Help:       def.Help,
			Group:      def.Group,
			Kind:       string(def.Kind),
			Value:      value,
			Default:    defaults[def.Key],
			Overridden: overridden[def.Key],
			Min:        def.Min,
			Max:        def.Max,
		})
	}
	return views, nil
}

func (svc *SettingsService) Update(
	ctx context.Context, in UpdateSettingsInput,
) ([]SettingView, error) {

	trial := svc.Runtime()
	changed := make([]string, 0, len(in.Set)+len(in.Reset))

	for key, raw := range in.Set {
		def, ok := settingDefOf(key)
		if !ok {
			return nil, domain.ErrValidation.WithMessage("未知的配置项：" + key)
		}
		if err := applySetting(def, raw, &trial); err != nil {
			return nil, domain.ErrValidation.WithMessage(err.Error())
		}
		changed = append(changed, key)
	}
	for _, key := range in.Reset {
		if _, ok := settingDefOf(key); !ok {
			return nil, domain.ErrValidation.WithMessage("未知的配置项：" + key)
		}
		changed = append(changed, key)
	}
	if len(changed) == 0 {
		return nil, domain.ErrValidation.WithMessage("没有要修改的配置项")
	}

	now := svc.nowUnix()
	err := svc.InTx(ctx, func(q *store.Queries) error {
		for key, raw := range in.Set {
			if err := q.UpsertSetting(ctx, store.UpsertSettingParams{
				Key:       key,
				Value:     strings.TrimSpace(raw),
				UpdatedAt: now,
			}); err != nil {
				return domain.ErrInternal.WithErr(err)
			}
		}
		for _, key := range in.Reset {
			if err := q.DeleteSetting(ctx, key); err != nil {
				return domain.ErrInternal.WithErr(err)
			}
		}
		svc.Log.InfoContext(ctx, "运行时配置已修改",
			"keys", strings.Join(changed, ", "), "actor", in.ActorID, "ip", in.IP)
		return nil
	})
	if err != nil {
		return nil, err
	}

	if err := svc.Reload(ctx); err != nil {
		return nil, err
	}
	return svc.List(ctx)
}
