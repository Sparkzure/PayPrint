package domain

type Tone string

const (
	ToneNeutral Tone = "neutral"
	TonePrimary Tone = "primary"
	ToneSuccess Tone = "success"
	ToneDanger  Tone = "danger"
	ToneWarning Tone = "warning"
)

type UserRole string

const (
	RoleUser UserRole = "user"

	RoleAdmin UserRole = "admin"
)

type UserStatus string

const (
	UserStatusActive UserStatus = "active"
	UserStatusBanned UserStatus = "banned"
)

func (r UserRole) IsAdmin() bool { return r == RoleAdmin }

func (r UserRole) Label() string {
	switch r {
	case RoleUser:
		return "普通用户"
	case RoleAdmin:
		return "管理员"
	default:
		return string(r)
	}
}

type OrderStatus string

const (
	OrderAwaitingPayment OrderStatus = "awaiting_payment"
	OrderAwaitingRelease OrderStatus = "awaiting_release"
	OrderQueued          OrderStatus = "queued"
	OrderPrinting        OrderStatus = "printing"
	OrderCompleted       OrderStatus = "completed"
	OrderFailed          OrderStatus = "failed"
	OrderRefundPending   OrderStatus = "refund_pending"

	OrderDisputed  OrderStatus = "disputed"
	OrderRefunded  OrderStatus = "refunded"
	OrderExpired   OrderStatus = "expired"
	OrderCancelled OrderStatus = "cancelled"
)

func (s OrderStatus) Label() string {
	switch s {
	case OrderAwaitingPayment:
		return "待支付"
	case OrderAwaitingRelease:
		return "待确认打印"
	case OrderQueued:
		return "排队中"
	case OrderPrinting:
		return "打印中"
	case OrderCompleted:
		return "已完成"
	case OrderFailed:
		return "打印失败"
	case OrderRefundPending:
		return "待退款"
	case OrderDisputed:
		return "异常待核"
	case OrderRefunded:
		return "已退款"
	case OrderExpired:
		return "已超时"
	case OrderCancelled:
		return "已取消"
	default:
		return string(s)
	}
}

func (s OrderStatus) Tone() Tone {
	switch s {
	case OrderAwaitingPayment:
		return ToneNeutral
	case OrderAwaitingRelease:

		return ToneWarning
	case OrderQueued, OrderPrinting:
		return TonePrimary
	case OrderCompleted:
		return ToneSuccess
	case OrderFailed:
		return ToneDanger
	case OrderRefundPending, OrderRefunded, OrderDisputed:
		return ToneWarning
	case OrderExpired, OrderCancelled:
		return ToneNeutral
	default:
		return ToneNeutral
	}
}

func (s OrderStatus) IsTerminal() bool {
	switch s {
	case OrderCompleted, OrderRefunded, OrderExpired, OrderCancelled:
		return true
	default:
		return false
	}
}

func (s OrderStatus) CanRelease() bool { return s == OrderAwaitingRelease }

func (s OrderStatus) CanPay() bool { return s == OrderAwaitingPayment }

func (s OrderStatus) IsReleased() bool {
	switch s {
	case OrderQueued, OrderPrinting, OrderCompleted:
		return true
	default:
		return false
	}
}

type JobStatus string

const (
	JobPending     JobStatus = "pending"
	JobSubmitting  JobStatus = "submitting"
	JobSubmitted   JobStatus = "submitted"
	JobPrinting    JobStatus = "printing"
	JobCompleted   JobStatus = "completed"
	JobFailed      JobStatus = "failed"
	JobNeedsReview JobStatus = "needs_review"

	JobInterrupted JobStatus = "interrupted"

	JobCancelled JobStatus = "cancelled"
)

func (s JobStatus) Label() string {
	switch s {
	case JobPending:
		return "排队中"
	case JobSubmitting:
		return "投递中"
	case JobSubmitted:
		return "已入队"
	case JobPrinting:
		return "打印中"
	case JobCompleted:
		return "已完成"
	case JobFailed:
		return "投递失败"
	case JobNeedsReview:
		return "待人工处理"
	case JobCancelled:
		return "已取消"
	case JobInterrupted:
		return "打印中断"
	default:
		return string(s)
	}
}

func (s JobStatus) Tone() Tone {
	switch s {
	case JobPending, JobSubmitting:
		return ToneNeutral
	case JobSubmitted, JobPrinting:
		return TonePrimary
	case JobCompleted:
		return ToneSuccess
	case JobFailed:
		return ToneDanger
	case JobNeedsReview, JobInterrupted:
		return ToneWarning
	case JobCancelled:
		return ToneNeutral
	default:
		return ToneNeutral
	}
}

type DeviceStatus string

const (
	DeviceActive   DeviceStatus = "active"
	DeviceUnbound  DeviceStatus = "unbound"
	DeviceRevoked  DeviceStatus = "revoked"
	DeviceReleased DeviceStatus = "released"
)

type LedgerType string

const (
	LedgerPayment LedgerType = "payment"
	LedgerRefund  LedgerType = "refund"
)

type PaymentEventStatus string

const (
	PaymentAccepted  PaymentEventStatus = "accepted"
	PaymentRejected  PaymentEventStatus = "rejected"
	PaymentDuplicate PaymentEventStatus = "duplicate"

	PaymentUnresolved PaymentEventStatus = "unresolved"
)

func (s PaymentEventStatus) Label() string {
	switch s {
	case PaymentAccepted:
		return "已放行"
	case PaymentRejected:
		return "已拒绝"
	case PaymentDuplicate:
		return "重复上报"
	case PaymentUnresolved:
		return "待人工处理"
	default:
		return string(s)
	}
}

func (s PaymentEventStatus) Tone() Tone {
	switch s {
	case PaymentAccepted:
		return ToneSuccess
	case PaymentRejected:
		return ToneDanger
	case PaymentUnresolved:
		return ToneWarning
	case PaymentDuplicate:
		return ToneNeutral
	default:
		return ToneNeutral
	}
}

type FilePageCountSource string

const (
	PageCountDetected FilePageCountSource = "detected"

	PageCountDeclared FilePageCountSource = "declared"
)

func (s FilePageCountSource) IsTrusted() bool { return s == PageCountDetected }

type PaymentChannel string

const (
	ChannelWeChat PaymentChannel = "wechat"
	ChannelAlipay PaymentChannel = "alipay"
	ChannelFree   PaymentChannel = "free"
)
