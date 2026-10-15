package domain

import (
	"errors"
	"fmt"
	"net/http"
)

type AppError struct {
	Code    string
	Message string
	Status  int
	Err     error
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *AppError) Unwrap() error { return e.Err }

func (e *AppError) WithErr(err error) *AppError {
	out := *e
	out.Err = err
	return &out
}

func (e *AppError) WithMessage(msg string) *AppError {
	out := *e
	out.Message = msg
	return &out
}

func New(code, message string, status int) *AppError {
	return &AppError{Code: code, Message: message, Status: status}
}

func AsAppError(err error) (*AppError, bool) {
	var ae *AppError
	if errors.As(err, &ae) {
		return ae, true
	}
	return nil, false
}

var (
	ErrInternal     = New("internal_error", "服务内部错误", http.StatusInternalServerError)
	ErrValidation   = New("validation_error", "请求参数不合法", http.StatusBadRequest)
	ErrNotFound     = New("not_found", "资源不存在", http.StatusNotFound)
	ErrUnauthorized = New("unauthorized", "未登录或会话已失效", http.StatusUnauthorized)
	ErrForbidden    = New("forbidden", "没有权限执行该操作", http.StatusForbidden)
	ErrConflict     = New("conflict", "操作与当前状态冲突", http.StatusConflict)
)

var (
	ErrInvalidCredentials = New("invalid_credentials", "用户名或密码不正确", http.StatusUnauthorized)
	ErrUsernameTaken      = New("username_taken", "该用户名已被占用", http.StatusConflict)
	ErrRegistrationClosed = New("registration_closed", "当前未开放注册", http.StatusForbidden)
	ErrAccountBanned      = New("account_banned", "账号已被封禁，请联系管理员", http.StatusForbidden)

	ErrDeviceAlreadyUsed = New("device_already_used",
		"该设备已参与过其他账号，一台设备仅能绑定一个账号", http.StatusConflict)
	ErrDeviceBoundToOther = New("device_bound_to_other_account",
		"该设备已绑定其他账号，如已更换使用者请联系管理员解绑", http.StatusConflict)

	ErrDeviceRevoked = New("device_revoked",
		"该设备已被管理员停用，请联系管理员", http.StatusForbidden)
	ErrDeviceNotFound    = New("device_not_found", "设备不存在", http.StatusNotFound)
	ErrDeviceKeyMismatch = New("device_key_mismatch",
		"设备公钥与首次登记不一致，可能存在设备伪造", http.StatusForbidden)
)

var (
	ErrMissingSignature = New("missing_signature", "缺少设备签名信息", http.StatusUnauthorized)
	ErrInvalidSignature = New("invalid_signature", "设备签名校验失败", http.StatusUnauthorized)
	ErrSignatureSkewed  = New("signature_expired", "请求时间戳超出允许窗口，请校准设备时间", http.StatusUnauthorized)
	ErrNonceReplayed    = New("nonce_replayed", "请求已被处理过（nonce 重复）", http.StatusConflict)
	ErrInvalidPublicKey = New("invalid_public_key", "设备公钥格式不正确", http.StatusBadRequest)
)

var (
	ErrFileNotFound    = New("file_not_found", "文件不存在或不属于当前账号", http.StatusNotFound)
	ErrFileTooLarge    = New("file_too_large", "文件超过大小上限", http.StatusRequestEntityTooLarge)
	ErrUnsupportedFile = New("unsupported_file_type",
		"仅支持 PDF 与图片（JPG / PNG）", http.StatusUnsupportedMediaType)
	ErrEmptyFile = New("empty_file", "文件内容为空", http.StatusBadRequest)

	ErrTooManyPages      = New("too_many_pages", "页数超过单次打印上限", http.StatusBadRequest)
	ErrPageRangeTooLarge = New("page_range_too_large",
		"页范围超出单次打印上限", http.StatusBadRequest)
)

var (
	ErrOrderNotFound   = New("order_not_found", "订单不存在", http.StatusNotFound)
	ErrPrinterNotFound = New("printer_not_found", "打印机不存在或未启用", http.StatusNotFound)
	ErrPrinterOffline  = New("printer_offline", "打印机当前不可用", http.StatusConflict)

	ErrPrinterClosed    = New("printer_closed", "打印机当前不在在线时段", http.StatusConflict)
	ErrOrderNotPayable  = New("order_not_payable", "订单当前状态不可支付", http.StatusConflict)
	ErrOrderExpired     = New("order_expired", "订单已超时关闭，请重新下单", http.StatusConflict)
	ErrParamUnsupported = New("param_unsupported", "所选打印机不支持该参数组合", http.StatusBadRequest)

	ErrTooManyPendingOrders = New("too_many_pending_orders",
		"待支付订单数量已达上限，请先完成或等待之前的订单关闭", http.StatusConflict)

	ErrAmountDuplicate = New("amount_duplicate",
		"金额与其他用户重复，请等待", http.StatusConflict)
	ErrNothingToPrint = New("nothing_to_print", "页范围选择结果为空", http.StatusBadRequest)

	ErrOrderNotReleasable = New("order_not_releasable",
		"订单当前状态不可确认打印", http.StatusConflict)
	ErrPaymentUnverified = New("payment_unverified",
		"支付尚未通过官方核验，暂不能打印", http.StatusConflict)
	ErrReleaseWindowClosed = New("release_window_closed",
		"已超过确认打印的时限，该订单已转待退款", http.StatusConflict)
)

var (
	ErrAmountMismatch = New("amount_mismatch", "上报金额与订单金额不一致", http.StatusConflict)

	ErrOrderNotOwned  = New("order_not_owned", "订单不属于该账号", http.StatusForbidden)
	ErrUnknownChannel = New("unknown_channel", "不支持的支付渠道", http.StatusBadRequest)

	ErrCollectorCredentialInvalid = New("collector_credential_invalid",
		"收款凭据无效或已吊销", http.StatusUnauthorized)
	ErrCollectorCredentialNotFound = New("collector_credential_not_found",
		"收款凭据不存在", http.StatusNotFound)

	ErrReportNeedsOrderNo = New("report_needs_order_no",
		"未提供收款凭据时必须携带订单号", http.StatusForbidden)

	ErrPaymentUnmatched = New("payment_unmatched",
		"该笔到账未匹配到任何待支付订单，已记录待人工处理", http.StatusConflict)

	ErrPaymentAmbiguous = New("payment_ambiguous",
		"同金额的候选订单不止一笔，无法确定归属，已转人工处理", http.StatusConflict)
)

var (
	ErrAdminCannotConsume = New("admin_cannot_consume",
		"管理员账号不能下单或打印，请使用普通用户账号", http.StatusForbidden)

	ErrAdminUseConsole = New("admin_use_console",
		"管理员账号请从管理面板登录", http.StatusForbidden)

	ErrNotAdminAccount = New("not_admin_account",
		"该账号不是管理员，请从用户入口登录", http.StatusForbidden)

	ErrSessionHasNoDevice = New("session_has_no_device",
		"当前会话没有绑定设备，不能执行该操作", http.StatusForbidden)
	ErrUserNotFound = New("user_not_found", "账号不存在", http.StatusNotFound)
)
