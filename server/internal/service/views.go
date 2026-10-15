package service

import (
	"encoding/json"
	"time"

	"github.com/Sparkzure/PayPrint/server/internal/domain"
	"github.com/Sparkzure/PayPrint/server/internal/store"
)

type UserView struct {
	ID            string `json:"id"`
	Username      string `json:"username"`
	Role          string `json:"role"`
	Status        string `json:"status"`
	StatusText    string `json:"status_text"`
	Tone          string `json:"tone"`
	TotalSpentFen int64  `json:"total_spent_fen"`
	CreatedAt     int64  `json:"created_at"`

	FreePrint bool `json:"free_print"`

	BanReason string `json:"ban_reason,omitempty"`
	BannedAt  int64  `json:"banned_at,omitempty"`
	BannedBy  string `json:"banned_by,omitempty"`
}

func NewUserView(u store.User) UserView {
	statusText := "正常"
	tone := domain.ToneSuccess
	if u.Status != string(domain.UserStatusActive) {
		statusText = "已封禁"
		tone = domain.ToneDanger
	}
	return UserView{
		ID:          u.ID,
		Username:    u.Username,
		Role:        u.Role,
		Status:      u.Status,
		StatusText:  statusText,
		Tone:        string(tone),
		CreatedAt:   u.CreatedAt,
		FreePrint:   u.FreePrint != 0,
		BanReason:   u.BanReason,
		BannedAt:    u.BannedAt,
		BannedBy:    u.BannedBy,
	}
}

type DeviceView struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	Platform   string `json:"platform"`
	AppVersion string `json:"app_version"`
	Status     string `json:"status"`
	StatusText string `json:"status_text"`
	Tone       string `json:"tone"`
	BoundAt    int64  `json:"bound_at"`
	LastSeenAt int64  `json:"last_seen_at"`
	CreatedAt  int64  `json:"created_at"`

	Current bool `json:"current"`

	UserID   string `json:"user_id,omitempty"`
	Username string `json:"username,omitempty"`
}

func NewDeviceView(d store.Device, currentDeviceID string) DeviceView {
	label := d.Label
	switch domain.DeviceStatus(d.Status) {
	case domain.DeviceActive:
		label = "已绑定"
	case domain.DeviceUnbound:
		label = "已解绑"
	case domain.DeviceRevoked:
		label = "已停用"
	case domain.DeviceReleased:

		label = "已释放"
	}
	tone := domain.ToneNeutral
	if d.Status == string(domain.DeviceActive) {
		tone = domain.ToneSuccess
	}
	if d.Status == string(domain.DeviceRevoked) {
		tone = domain.ToneDanger
	}
	return DeviceView{
		ID:         d.ID,
		Label:      d.Label,
		Platform:   d.Platform,
		AppVersion: d.AppVersion,
		Status:     d.Status,
		StatusText: label,
		Tone:       string(tone),
		BoundAt:    d.BoundAt,
		LastSeenAt: d.LastSeenAt,
		CreatedAt:  d.CreatedAt,
		Current:    d.ID == currentDeviceID,
		UserID:     deref(d.UserID),
	}
}

func DeviceViewOrNil(d store.Device, currentDeviceID string) *DeviceView {
	if d.ID == "" {
		return nil
	}
	view := NewDeviceView(d, currentDeviceID)
	return &view
}

func (v DeviceView) WithOwner(u store.User) DeviceView {
	v.UserID = u.ID
	v.Username = u.Username
	return v
}

type CollectorView struct {
	ID           string `json:"id"`
	PrinterID    string `json:"printer_id"`
	PrinterCode  string `json:"printer_code"`
	PrinterName  string `json:"printer_name"`
	Label        string `json:"label"`
	CreatedAt    int64  `json:"created_at"`
	LastUsedAt   int64  `json:"last_used_at"`
	Revoked      bool   `json:"revoked"`
	RevokedAt    int64  `json:"revoked_at,omitempty"`
	RevokeReason string `json:"revoke_reason,omitempty"`
	StatusText   string `json:"status_text"`
	Tone         string `json:"tone"`
}

func NewCollectorView(c store.CollectorCredential) CollectorView {
	revoked := c.RevokedAt != 0
	statusText := "生效中"
	tone := domain.ToneSuccess
	if revoked {
		statusText = "已吊销"
		tone = domain.ToneDanger
	}
	view := CollectorView{
		ID:           c.ID,
		PrinterID:    c.PrinterID,
		Label:        c.Label,
		CreatedAt:    c.CreatedAt,
		LastUsedAt:   c.LastUsedAt,
		Revoked:      revoked,
		RevokedAt:    c.RevokedAt,
		RevokeReason: c.RevokeReason,
		StatusText:   statusText,
		Tone:         string(tone),
	}
	return view
}

func (v CollectorView) WithPrinter(p store.Printer) CollectorView {
	v.PrinterID = p.ID
	v.PrinterCode = p.Code
	v.PrinterName = p.Name
	return v
}

type IssuedCollectorView struct {
	CollectorView
	Token string `json:"token"`
}

type PrinterView struct {
	ID         string `json:"id"`
	Code       string `json:"code"`
	Name       string `json:"name"`
	Location   string `json:"location"`
	Status     string `json:"status"`
	StatusText string `json:"status_text"`
	Tone       string `json:"tone"`

	PriceSingleBWFen     int64  `json:"price_single_bw_fen"`
	PriceSingleColorFen  int64  `json:"price_single_color_fen"`
	PriceDuplexBWFen     int64  `json:"price_duplex_bw_fen"`
	PriceDuplexColorFen  int64  `json:"price_duplex_color_fen"`
	PriceSingleBWText    string `json:"price_single_bw_text"`
	PriceSingleColorText string `json:"price_single_color_text"`
	PriceDuplexBWText    string `json:"price_duplex_bw_text"`
	PriceDuplexColorText string `json:"price_duplex_color_text"`

	SupportsColor  bool     `json:"supports_color"`
	SupportsDuplex bool     `json:"supports_duplex"`
	PaperSizes     []string `json:"paper_sizes"`
	MaxCopies      int      `json:"max_copies"`

	OnlineWindows []OnlineWindow `json:"online_windows"`

	OnlineWindowsText string `json:"online_windows_text"`

	WithinSchedule bool `json:"within_schedule"`

	Live *PrinterLiveView `json:"live,omitempty"`

	CupsURI string `json:"cups_uri,omitempty"`
}

type PrinterLiveView struct {
	Reachable     bool     `json:"reachable"`
	State         string   `json:"state"`
	StateReasons  []string `json:"state_reasons"`
	StateMessage  string   `json:"state_message"`
	AcceptingJobs bool     `json:"accepting_jobs"`
	MakeAndModel  string   `json:"make_and_model"`
	Error         string   `json:"error,omitempty"`
}

type PrintFileView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	PageCount   int    `json:"page_count"`
	CreatedAt   int64  `json:"created_at"`
}

func NewPrintFileView(f store.PrintFile) PrintFileView {
	return PrintFileView{
		ID:          f.ID,
		Name:        f.OriginalName,
		ContentType: f.ContentType,
		SizeBytes:   f.SizeBytes,
		PageCount:   int(f.PageCount),
		CreatedAt:   f.CreatedAt,
	}
}

type OrderView struct {
	ID         string `json:"id"`
	OrderNo    string `json:"order_no"`
	Status     string `json:"status"`
	StatusText string `json:"status_text"`
	Tone       string `json:"tone"`
	AmountFen  int64  `json:"amount_fen"`
	AmountText string `json:"amount_text"`
	Currency   string `json:"currency"`
	Title      string `json:"title"`
	IsFree     bool   `json:"is_free"`

	PrinterID       string `json:"printer_id"`
	PrinterCode     string `json:"printer_code"`
	PrinterName     string `json:"printer_name"`
	PrinterLocation string `json:"printer_location"`

	UserID   string `json:"user_id,omitempty"`
	Username string `json:"username,omitempty"`

	FileID        string `json:"file_id"`
	FileName      string `json:"file_name"`
	FilePageCount int    `json:"file_page_count"`

	Params *domain.PrintParams   `json:"params,omitempty"`
	Cost   *domain.CostBreakdown `json:"cost,omitempty"`

	JobStatus     string `json:"job_status"`
	JobStatusText string `json:"job_status_text"`
	JobTone       string `json:"job_tone"`
	JobState      string `json:"job_state"`
	CupsJobID     int64  `json:"cups_job_id"`
	JobAttempts   int    `json:"job_attempts"`
	JobError      string `json:"job_error"`

	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
	ExpiresAt   int64  `json:"expires_at"`
	PaidAt      int64  `json:"paid_at"`
	CompletedAt int64  `json:"completed_at"`
	CloseReason string `json:"close_reason"`

	CanPay    bool `json:"can_pay"`
	CanCancel bool `json:"can_cancel"`

	CanRelease bool `json:"can_release"`

	RemainingSeconds int64 `json:"remaining_seconds"`

	JustReleased bool `json:"just_released,omitempty"`
}

func NewOrderView(o store.Order) OrderView {
	status := domain.OrderStatus(o.Status)
	view := OrderView{
		ID:          o.ID,
		OrderNo:     o.OrderNo,
		Status:      o.Status,
		StatusText:  status.Label(),
		Tone:        string(status.Tone()),
		AmountFen:   o.AmountFen,
		AmountText:  domain.FormatYuan(o.AmountFen),
		Currency:    o.Currency,
		Title:       o.Title,
		IsFree:      o.AmountFen == 0,
		PrinterID:   o.PrinterID,
		FileID:      o.FileID,
		CreatedAt:   o.CreatedAt,
		UpdatedAt:   o.UpdatedAt,
		ExpiresAt:   o.ExpiresAt,
		PaidAt:      o.PaidAt,
		CompletedAt: o.CompletedAt,
		CloseReason: o.CloseReason,
		CanPay:      status.CanPay(),
		CanCancel:   status.CanPay(),
		CanRelease:  status.CanRelease(),
	}
	if o.ParamsJson != "" {
		var params domain.PrintParams
		if err := json.Unmarshal([]byte(o.ParamsJson), &params); err == nil {
			view.Params = &params
		}
	}
	if o.CostJson != "" {
		var cost domain.CostBreakdown
		if err := json.Unmarshal([]byte(o.CostJson), &cost); err == nil {
			view.Cost = &cost
		}
	}
	return view
}

func (v OrderView) WithPrinter(p store.Printer) OrderView {
	v.PrinterID = p.ID
	v.PrinterCode = p.Code
	v.PrinterName = p.Name
	v.PrinterLocation = p.Location
	return v
}

func (v OrderView) WithFile(f store.PrintFile) OrderView {
	v.FileID = f.ID
	v.FileName = f.OriginalName
	v.FilePageCount = int(f.PageCount)
	return v
}

func (v OrderView) WithJob(j *store.PrintJob) OrderView {
	if j == nil {
		v.JobStatus = ""
		v.JobStatusText = ""
		v.JobTone = ""
		v.JobState = ""
		v.CupsJobID = 0
		v.JobAttempts = 0
		v.JobError = ""
		return v
	}
	js := domain.JobStatus(j.Status)
	v.JobStatus = j.Status
	v.JobStatusText = js.Label()
	v.JobTone = string(js.Tone())
	v.JobState = j.JobState
	v.CupsJobID = j.CupsJobID
	v.JobAttempts = int(j.Attempts)
	v.JobError = j.LastError
	return v
}

func (v OrderView) WithTiming(orderTTL, releaseTTL time.Duration, now time.Time) OrderView {
	var deadline int64

	switch domain.OrderStatus(v.Status) {
	case domain.OrderAwaitingPayment:
		deadline = v.ExpiresAt
	case domain.OrderAwaitingRelease:
		if releaseTTL > 0 && v.PaidAt > 0 {
			deadline = v.PaidAt + int64(releaseTTL.Seconds())
		}
	}

	if deadline <= 0 {
		return v
	}
	remaining := deadline - now.Unix()
	if remaining < 0 {
		remaining = 0
	}
	v.RemainingSeconds = remaining
	return v
}

type PaymentEventView struct {
	ID         string `json:"id"`
	EventID    string `json:"event_id"`
	Source     string `json:"source"`
	Channel    string `json:"channel"`
	OrderID    string `json:"order_id,omitempty"`
	OrderNo    string `json:"order_no"`
	UserID     string `json:"user_id,omitempty"`
	Username   string `json:"username,omitempty"`
	OutTradeNo string `json:"out_trade_no"`
	AmountFen  int64  `json:"amount_fen"`
	AmountText string `json:"amount_text"`
	Status     string `json:"status"`
	StatusText string `json:"status_text"`
	Tone       string `json:"tone"`

	Reason     string `json:"reason"`
	ReportedAt int64  `json:"reported_at"`
	CreatedAt  int64  `json:"created_at"`
}

func NewPaymentEventView(e store.PaymentEvent) PaymentEventView {
	status := domain.PaymentEventStatus(e.Status)
	return PaymentEventView{
		ID:         e.ID,
		EventID:    e.EventID,
		Source:     e.Source,
		Channel:    e.Channel,
		OrderID:    deref(e.OrderID),
		OrderNo:    e.OrderNo,
		UserID:     deref(e.UserID),
		OutTradeNo: e.OutTradeNo,
		AmountFen:  e.AmountFen,
		AmountText: domain.FormatYuan(e.AmountFen),
		Status:     e.Status,
		StatusText: status.Label(),
		Tone:       string(status.Tone()),
		Reason:     e.RejectReason,
		ReportedAt: e.ReportedAt,
		CreatedAt:  e.CreatedAt,
	}
}

func (v PaymentEventView) WithOwner(username string) PaymentEventView {
	v.Username = username
	return v
}

type OrderAuditView struct {
	ID      string `json:"id"`
	OrderID string `json:"order_id"`
	OrderNo string `json:"order_no"`

	UserID   string `json:"user_id"`
	Username string `json:"username"`

	DeviceLabel       string `json:"device_label"`
	DeviceFingerprint string `json:"device_fingerprint"`

	AmountFen  int64  `json:"amount_fen"`
	AmountText string `json:"amount_text"`
	FileName   string `json:"file_name"`

	ColorMode  string `json:"color_mode"`
	ColorText  string `json:"color_text"`
	Duplex     string `json:"duplex"`
	DuplexText string `json:"duplex_text"`
	Copies     int64  `json:"copies"`
	TotalPages int64  `json:"total_pages"`

	CreatedAt int64 `json:"created_at"`
}

func NewOrderAuditView(a store.OrderAudit) OrderAuditView {
	return OrderAuditView{
		ID:                a.ID,
		OrderID:           a.OrderID,
		OrderNo:           a.OrderNo,
		UserID:            a.UserID,
		Username:          a.Username,
		DeviceLabel:       a.DeviceLabel,
		DeviceFingerprint: a.DeviceFingerprint,
		AmountFen:         a.AmountFen,
		AmountText:        domain.FormatYuan(a.AmountFen),
		FileName:          a.FileName,
		ColorMode:         a.ColorMode,
		ColorText:         domain.ColorMode(a.ColorMode).Label(),
		Duplex:            a.Duplex,
		DuplexText:        domain.DuplexMode(a.Duplex).Label(),
		Copies:            a.Copies,
		TotalPages:        a.TotalPages,
		CreatedAt:         a.CreatedAt,
	}
}

type LedgerView struct {
	ID            string `json:"id"`
	OrderID       string `json:"order_id"`
	EntryType     string `json:"entry_type"`
	TypeText      string `json:"type_text"`
	Tone          string `json:"tone"`
	AmountFen     int64  `json:"amount_fen"`
	AmountText    string `json:"amount_text"`
	TotalSpentFen int64  `json:"total_spent_fen"`
	Channel       string `json:"channel"`
	Memo          string `json:"memo"`
	CreatedAt     int64  `json:"created_at"`
}

func NewLedgerView(e store.LedgerEntry) LedgerView {
	typeText := "消费"
	tone := domain.ToneNeutral
	switch domain.LedgerType(e.EntryType) {
	case domain.LedgerPayment:
		typeText = "支出"
	case domain.LedgerRefund:
		typeText = "退款"
		tone = domain.ToneSuccess
	}
	return LedgerView{
		ID:            e.ID,
		OrderID:       e.OrderID,
		EntryType:     e.EntryType,
		TypeText:      typeText,
		Tone:          string(tone),
		AmountFen:     e.AmountFen,
		AmountText:    domain.FormatYuan(e.AmountFen),
		TotalSpentFen: e.SpentAfterFen,
		Channel:       e.Channel,
		Memo:          e.Memo,
		CreatedAt:     e.CreatedAt,
	}
}
