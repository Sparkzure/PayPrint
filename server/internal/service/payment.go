package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Sparkzure/PayPrint/server/internal/domain"
	"github.com/Sparkzure/PayPrint/server/internal/ids"
	"github.com/Sparkzure/PayPrint/server/internal/notifier"
	"github.com/Sparkzure/PayPrint/server/internal/store"
)

type PaymentService struct{ *Services }

type ReportInput struct {
	Report *notifier.Report

	UserID string

	CollectorPrinterID string
	IP                 string
}

type ReportResult struct {
	Accepted    bool   `json:"accepted"`
	Duplicate   bool   `json:"duplicate"`
	OrderID     string `json:"order_id,omitempty"`
	OrderNo     string `json:"order_no"`
	OrderStatus string `json:"order_status"`
	StatusText  string `json:"status_text"`
	Reason      string `json:"reason,omitempty"`

	JobQueued bool `json:"job_queued"`

	CanRelease bool `json:"can_release"`

	VerifyPending bool `json:"verify_pending"`
}

func (svc *PaymentService) Report(ctx context.Context, in ReportInput) (*ReportResult, error) {
	report := in.Report
	if report == nil {
		return nil, domain.ErrValidation.WithMessage("缺少支付上报内容")
	}

	rep, err := svc.resolveReporter(in)
	if err != nil {
		return nil, err
	}

	if existing, err := svc.Q.GetPaymentEventByEventID(ctx, report.EventID); err == nil {
		return &ReportResult{
			Accepted:    existing.Status == string(domain.PaymentAccepted),
			Duplicate:   true,
			OrderID:     deref(existing.OrderID),
			OrderNo:     existing.OrderNo,
			OrderStatus: "",
			StatusText:  "该上报已处理过",
			Reason:      existing.RejectReason,
		}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrInternal.WithErr(err)
	}

	order, err := svc.locateOrder(ctx, report, rep)
	if err != nil {
		return nil, err
	}

	userID := rep.userID
	if userID == "" {
		userID = order.UserID
	}

	if strings.TrimSpace(report.OrderNo) == "" {
		report.OrderNo = order.OrderNo
	}

	status := domain.OrderStatus(order.Status)

	if status == domain.OrderAwaitingRelease || status.IsReleased() {
		svc.recordDuplicate(ctx, report, &order, order.UserID)
		return &ReportResult{
			Accepted:    true,
			Duplicate:   true,
			OrderID:     order.ID,
			OrderNo:     order.OrderNo,
			OrderStatus: order.Status,
			StatusText:  status.Label(),
			Reason:      "该订单的支付此前已确认",
			JobQueued:   status.IsReleased(),
			CanRelease:  status.CanRelease(),
		}, nil
	}

	if status != domain.OrderAwaitingPayment {
		svc.rejectEvent(ctx, report, userID, order.ID, "订单状态不可支付: "+order.Status)
		return nil, domain.ErrOrderNotPayable
	}

	if order.ExpiresAt <= svc.nowUnix() {
		if _, cerr := svc.Q.CloseOrder(ctx, store.CloseOrderParams{
			Status:      string(domain.OrderExpired),
			ClosedAt:    svc.nowUnix(),
			CloseReason: "支付到账时订单已超时",
			UpdatedAt:   svc.nowUnix(),
			ID:          order.ID,
		}); cerr != nil {
			svc.Log.WarnContext(ctx, "关闭超时订单失败", "order_id", order.ID, "error", cerr)
		}
		svc.unresolvedEvent(ctx, report, userID, order.ID, "订单已超时")
		return nil, domain.ErrOrderExpired
	}

	if report.AmountFen != order.AmountFen {
		svc.unresolvedEvent(ctx, report, userID, order.ID, fmt.Sprintf(
			"金额不一致：上报 %d 分，订单 %d 分", report.AmountFen, order.AmountFen))
		return nil, domain.ErrAmountMismatch
	}

	if !isKnownChannel(report.Channel) {
		svc.rejectEvent(ctx, report, userID, order.ID, "未知支付渠道: "+string(report.Channel))
		return nil, domain.ErrUnknownChannel
	}

	now := svc.now()
	autoRelease := svc.Runtime().AutoReleaseOnPayment
	nextStatus := domain.OrderAwaitingRelease
	if autoRelease {
		nextStatus = domain.OrderQueued
	}

	result := &ReportResult{
		Accepted:      true,
		OrderID:       order.ID,
		OrderNo:       order.OrderNo,
		OrderStatus:   string(nextStatus),
		StatusText:    nextStatus.Label(),
		JobQueued:     autoRelease,
		CanRelease:    !autoRelease,
		VerifyPending: true,
	}

	err = svc.InTx(ctx, func(q *store.Queries) error {
		if _, err := q.CreatePaymentEvent(ctx, store.CreatePaymentEventParams{
			ID:           ids.New(),
			EventID:      report.EventID,
			Source:       report.Source,
			Channel:      string(report.Channel),
			OrderID:      &order.ID,
			OrderNo:      order.OrderNo,
			UserID:       &userID,
			OutTradeNo:   report.OutTradeNo,
			AmountFen:    report.AmountFen,
			Status:       string(domain.PaymentAccepted),
			RejectReason: "",
			ReportedAt:   report.PaidAt,
			ClientTs:     report.Timestamp,
			Nonce:        report.Nonce,
			RawPayload:   truncate(string(report.Raw), 4096),
			CreatedAt:    now.Unix(),
		}); err != nil {

			if isUniqueViolation(err) {
				return errDuplicateEvent
			}
			return domain.ErrInternal.WithErr(err)
		}

		released, err := q.MarkOrderPaid(ctx, store.MarkOrderPaidParams{
			PaidAt:    now.Unix(),
			UpdatedAt: now.Unix(),
			ID:        order.ID,
		})
		if err != nil {
			return domain.ErrInternal.WithErr(err)
		}
		if released == 0 {

			return errDuplicateEvent
		}

		spent, err := q.SumSpentForUser(ctx, order.UserID)
		if err != nil {
			return domain.ErrInternal.WithErr(err)
		}
		spent += order.AmountFen
		if _, err := q.CreateLedgerEntry(ctx, store.CreateLedgerEntryParams{
			ID:            ids.New(),
			OrderID:       order.ID,
			UserID:        order.UserID,
			EntryType:     string(domain.LedgerPayment),
			AmountFen:     order.AmountFen,
			SpentAfterFen: spent,
			Channel:       string(report.Channel),
			Memo:          fmt.Sprintf("%s 支付到账（%s）", order.OrderNo, report.Source),
			CreatedAt:     now.Unix(),
		}); err != nil {
			return domain.ErrInternal.WithErr(err)
		}

		if autoRelease {

			if _, err := svc.Jobs.enqueue(ctx, q, order, now); err != nil {
				return err
			}
			queued, err := q.MarkOrderQueued(ctx, store.MarkOrderQueuedParams{
				UpdatedAt: now.Unix(),
				ID:        order.ID,
			})
			if err != nil {
				return domain.ErrInternal.WithErr(err)
			}
			if queued == 0 {
				return errDuplicateEvent
			}
		}

		if _, err := q.CreateOrderVerification(ctx, store.CreateOrderVerificationParams{
			ID:          ids.New(),
			OrderID:     order.ID,
			Channel:     string(report.Channel),
			OutTradeNo:  report.OutTradeNo,
			MaxAttempts: int64(svc.Cfg.WeChat.MaxAttemp),
			NextRetryAt: now.Unix(),
			CreatedAt:   now.Unix(),
			UpdatedAt:   now.Unix(),
		}); err != nil {
			return domain.ErrInternal.WithErr(err)
		}

		return nil
	})

	if err != nil {
		if errors.Is(err, errDuplicateEvent) {

			return &ReportResult{
				Accepted:    true,
				Duplicate:   true,
				OrderID:     order.ID,
				OrderNo:     order.OrderNo,
				OrderStatus: string(domain.OrderAwaitingRelease),
				StatusText:  domain.OrderAwaitingRelease.Label(),
				Reason:      "并发重复上报，按幂等处理",
				CanRelease:  !autoRelease,
				JobQueued:   autoRelease,
			}, nil
		}
		return nil, err
	}

	svc.Log.InfoContext(ctx, "支付上报已确认，订单等待用户确认打印",
		"order_no", order.OrderNo,
		"amount_fen", order.AmountFen,
		"channel", report.Channel,
		"auto_release", autoRelease)
	return result, nil
}

func (svc *PaymentService) EnsurePaymentVerified(ctx context.Context, order store.Order) error {

	if order.AmountFen == 0 {
		return nil
	}

	verification, err := svc.Q.GetVerificationByOrder(ctx, order.ID)
	if err != nil {

		if svc.Runtime().RequirePaymentVerification {
			return domain.ErrPaymentUnverified.WithMessage("该订单没有支付核验记录，无法放行打印")
		}
		return nil
	}

	querier := svc.Queriers.For(verification.Channel)
	if _, notConfigured := querier.(notifier.NopQuerier); notConfigured {
		if svc.Runtime().RequirePaymentVerification {
			return domain.ErrPaymentUnverified.WithMessage(
				"服务端未配置官方查单，无法完成放行前核验，请联系管理员")
		}
		svc.Log.DebugContext(ctx, "未配置官方查单，放行前核验跳过", "order_no", order.OrderNo)
		return nil
	}

	callCtx, cancel := context.WithTimeout(ctx, svc.Cfg.WeChat.Timeout)
	result, err := querier.Query(callCtx, order.OrderNo, verification.OutTradeNo)
	cancel()
	if err != nil {
		if svc.Runtime().RequirePaymentVerification {
			return domain.ErrPaymentUnverified.
				WithMessage("支付核验请求失败，请稍后重试").WithErr(err)
		}
		svc.Log.WarnContext(ctx, "放行前核验请求失败，按已通过的上报校验链放行",
			"order_no", order.OrderNo, "error", err)
		return nil
	}

	switch result.State {
	case notifier.QueryPaid:
		if result.AmountFen != 0 && result.AmountFen != order.AmountFen {
			reason := fmt.Sprintf("官方查单金额 %d 分与订单 %d 分不一致（放行前核验）",
				result.AmountFen, order.AmountFen)
			svc.markVerificationMismatch(ctx, verification, reason, result.Raw)
			svc.markOrderDisputed(ctx, order, reason)
			return domain.ErrAmountMismatch.WithMessage("官方查单金额与订单不一致，已转人工复核")
		}

		if _, err := svc.Q.FinishVerification(ctx, store.FinishVerificationParams{
			Status:    "confirmed",
			Result:    truncate(result.Raw, 4096),
			LastError: "",
			UpdatedAt: svc.nowUnix(),
			ID:        verification.ID,
		}); err != nil {
			svc.Log.WarnContext(ctx, "标记核验通过失败", "verification_id", verification.ID, "error", err)
		}
		return nil

	case notifier.QueryUnknown:
		if svc.Runtime().RequirePaymentVerification {
			return domain.ErrPaymentUnverified.WithMessage("官方查单未返回明确结论，请稍后重试")
		}
		svc.Log.WarnContext(ctx, "放行前核验未获得明确结论，按已通过的上报校验链放行",
			"order_no", order.OrderNo, "trade_state", result.TradeState)
		return nil

	default:
		reason := fmt.Sprintf("官方查单结论为 %s（trade_state=%s），与设备上报矛盾（放行前核验）",
			result.State, result.TradeState)
		svc.markVerificationMismatch(ctx, verification, reason, result.Raw)
		svc.markOrderDisputed(ctx, order, reason)
		return domain.ErrPaymentUnverified.WithMessage("官方查单显示该笔支付未成立，已转人工复核")
	}
}

func (svc *PaymentService) markVerificationMismatch(
	ctx context.Context, verification store.OrderVerification, reason, raw string,
) {
	if _, err := svc.Q.FinishVerification(ctx, store.FinishVerificationParams{
		Status:    "mismatch",
		Result:    truncate(raw, 4096),
		LastError: truncate(reason, 512),
		UpdatedAt: svc.nowUnix(),
		ID:        verification.ID,
	}); err != nil {
		svc.Log.WarnContext(ctx, "标记核验矛盾失败", "verification_id", verification.ID, "error", err)
	}
}

var errDuplicateEvent = errors.New("duplicate payment event")

func (svc *PaymentService) recordEvent(
	ctx context.Context, report *notifier.Report, userID, orderID string,
	status domain.PaymentEventStatus, reason string,
) {
	params := store.CreatePaymentEventParams{
		ID:           ids.New(),
		EventID:      report.EventID,
		Source:       report.Source,
		Channel:      string(report.Channel),
		OrderNo:      report.OrderNo,
		OutTradeNo:   report.OutTradeNo,
		AmountFen:    report.AmountFen,
		Status:       string(status),
		RejectReason: truncate(reason, 512),
		ReportedAt:   report.PaidAt,
		ClientTs:     report.Timestamp,
		Nonce:        report.Nonce,
		RawPayload:   truncate(string(report.Raw), 4096),
		CreatedAt:    svc.nowUnix(),
	}

	if userID != "" {
		params.UserID = &userID
	}
	if orderID != "" {
		params.OrderID = &orderID
	}

	if _, err := svc.Q.CreatePaymentEvent(ctx, params); err != nil {
		svc.Log.WarnContext(ctx, "记录支付上报事件失败",
			"event_id", report.EventID, "status", status, "reason", reason, "error", err)
	}
}

func (svc *PaymentService) rejectEvent(
	ctx context.Context, report *notifier.Report, userID, orderID, reason string,
) {
	svc.recordEvent(ctx, report, userID, orderID, domain.PaymentRejected, reason)
	svc.Log.WarnContext(ctx, "支付上报被拒",
		"order_no", report.OrderNo, "event_id", report.EventID, "reason", reason)
}

func (svc *PaymentService) unresolvedEvent(
	ctx context.Context, report *notifier.Report, userID, orderID, reason string,
) {
	svc.recordEvent(ctx, report, userID, orderID, domain.PaymentUnresolved, reason)
	svc.Log.ErrorContext(ctx, "到账未能归属到订单，需人工处理",
		"order_no", report.OrderNo, "event_id", report.EventID,
		"amount_fen", report.AmountFen, "reason", reason)
}

func (svc *PaymentService) recordDuplicate(
	ctx context.Context, report *notifier.Report, order *store.Order, userID string,
) {
	params := store.CreatePaymentEventParams{
		ID:           ids.New(),
		EventID:      report.EventID,
		Source:       report.Source,
		Channel:      string(report.Channel),
		OrderID:      &order.ID,
		OrderNo:      report.OrderNo,
		OutTradeNo:   report.OutTradeNo,
		AmountFen:    report.AmountFen,
		Status:       string(domain.PaymentDuplicate),
		RejectReason: "",
		ReportedAt:   report.PaidAt,
		ClientTs:     report.Timestamp,
		Nonce:        report.Nonce,
		RawPayload:   truncate(string(report.Raw), 4096),
		CreatedAt:    svc.nowUnix(),
	}
	if userID != "" {
		params.UserID = &userID
	}

	if _, err := svc.Q.CreatePaymentEvent(ctx, params); err != nil {
		svc.Log.WarnContext(ctx, "记录重复支付上报失败",
			"event_id", report.EventID, "error", err)
	}
}

func isKnownChannel(c domain.PaymentChannel) bool {
	switch c {
	case domain.ChannelWeChat, domain.ChannelAlipay, domain.ChannelFree:
		return true
	}
	return false
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

type reporter struct {
	userID string

	collectorPrinterID string
}

func (r reporter) IsCollector() bool { return r.collectorPrinterID != "" }

func (svc *PaymentService) resolveReporter(in ReportInput) (reporter, error) {
	switch {
	case in.UserID != "":
		return reporter{userID: in.UserID}, nil
	case in.CollectorPrinterID != "":
		return reporter{collectorPrinterID: in.CollectorPrinterID}, nil
	default:
		return reporter{}, domain.ErrUnauthorized.WithMessage(
			"支付上报必须由已登录账号或收款凭据发起")
	}
}

func (svc *PaymentService) locateOrder(
	ctx context.Context, report *notifier.Report, rep reporter,
) (store.Order, error) {
	if orderNo := strings.TrimSpace(report.OrderNo); orderNo != "" {
		return svc.locateByOrderNo(ctx, report, rep, orderNo)
	}
	if !rep.IsCollector() {

		svc.rejectEvent(ctx, report, rep.userID, "", "未提供收款凭据且未提供订单号")
		return store.Order{}, domain.ErrReportNeedsOrderNo
	}
	return svc.locateByAmount(ctx, report, rep)
}

func (svc *PaymentService) locateByOrderNo(
	ctx context.Context, report *notifier.Report, rep reporter, orderNo string,
) (store.Order, error) {
	order, err := svc.Q.GetOrderByNo(ctx, orderNo)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			svc.rejectEvent(ctx, report, rep.userID, "", "订单不存在")
			return store.Order{}, domain.ErrOrderNotFound
		}
		return store.Order{}, domain.ErrInternal.WithErr(err)
	}

	var (
		owned  bool
		reason string
	)
	switch {
	case rep.IsCollector():

		owned = order.PrinterID == rep.collectorPrinterID
		reason = "订单不属于该收款凭据绑定的打印机"
	default:

		owned = order.UserID == rep.userID
		reason = "订单不属于该账号"
	}

	if owned {
		return order, nil
	}
	svc.rejectEvent(ctx, report, rep.userID, order.ID, reason)
	return store.Order{}, domain.ErrOrderNotOwned.WithMessage(reason)
}

func (svc *PaymentService) locateByAmount(
	ctx context.Context, report *notifier.Report, rep reporter,
) (store.Order, error) {
	now := svc.nowUnix()

	createdBefore := now
	if report.PaidAt > 0 {
		createdBefore = report.PaidAt + int64(svc.Cfg.SignatureSkew.Seconds())
	}

	candidates, err := svc.Q.FindPaymentMatchCandidates(ctx, store.FindPaymentMatchCandidatesParams{
		PrinterID: rep.collectorPrinterID,
		AmountFen: report.AmountFen,
		CreatedAt: createdBefore,
		ExpiresAt: now,
	})
	if err != nil {
		return store.Order{}, domain.ErrInternal.WithErr(err)
	}

	candidates = filterCandidatesBySpec(candidates, report)

	switch len(candidates) {
	case 1:
		return candidates[0], nil
	case 0:
		return store.Order{}, svc.rejectUnmatched(ctx, report, rep, now)
	default:
		svc.unresolvedEvent(ctx, report, rep.userID, "", fmt.Sprintf(
			"同打印机同金额的候选订单有 %d 笔，无法确定归属", len(candidates)))
		return store.Order{}, domain.ErrPaymentAmbiguous
	}
}

func filterCandidatesBySpec(orders []store.Order, report *notifier.Report) []store.Order {
	if report.Duplex == "" && report.ColorMode == "" && report.UnitPriceFen <= 0 {
		return orders
	}
	out := make([]store.Order, 0, len(orders))
	for _, o := range orders {
		var params domain.PrintParams
		if strings.TrimSpace(o.ParamsJson) != "" {
			if err := json.Unmarshal([]byte(o.ParamsJson), &params); err != nil {
				continue
			}
		}
		if report.Duplex != "" && params.Duplex != report.Duplex {
			continue
		}
		if report.ColorMode != "" && params.ColorMode != report.ColorMode {
			continue
		}
		if report.UnitPriceFen > 0 {
			var cost domain.CostBreakdown
			if strings.TrimSpace(o.CostJson) != "" {
				if err := json.Unmarshal([]byte(o.CostJson), &cost); err != nil {
					continue
				}
			}
			if cost.UnitPriceFen != report.UnitPriceFen {
				continue
			}
		}
		out = append(out, o)
	}
	return out
}

func (svc *PaymentService) rejectUnmatched(
	ctx context.Context, report *notifier.Report, rep reporter, now int64,
) error {

	anchor := now
	if report.PaidAt > 0 && report.PaidAt < anchor {
		anchor = report.PaidAt
	}
	expired, err := svc.Q.FindExpiredOrdersByAmount(ctx, store.FindExpiredOrdersByAmountParams{
		PrinterID: rep.collectorPrinterID,
		AmountFen: report.AmountFen,
		ClosedAt:  anchor - int64(svc.Runtime().OrderTTL.Seconds()),
		ExpiresAt: now,
	})
	if err != nil {
		svc.Log.WarnContext(ctx, "查询同金额超时订单失败",
			"printer_id", rep.collectorPrinterID, "error", err)
	}

	if len(expired) > 0 {
		last := expired[0]
		svc.unresolvedEvent(ctx, report, rep.userID, last.ID, fmt.Sprintf(
			"到账时间已超出订单 %s 的支付时限", last.OrderNo))
		return domain.ErrOrderExpired.WithMessage(fmt.Sprintf(
			"该笔到账对应的订单 %s 已超出支付时限，已记录待人工退款", last.OrderNo))
	}

	svc.unresolvedEvent(ctx, report, rep.userID, "", "未匹配到任何待支付订单")
	return domain.ErrPaymentUnmatched
}
