package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Sparkzure/PayPrint/server/internal/domain"
	"github.com/Sparkzure/PayPrint/server/internal/ids"
	"github.com/Sparkzure/PayPrint/server/internal/store"
)

type OrderService struct{ *Services }

type CreateOrderInput struct {
	UserID            string
	DeviceLabel       string
	DeviceFingerprint string
	FileID            string
	PrinterID         string
	Params            domain.PrintParams
	Title             string
	IsTest            bool
	IP                string
}

func (svc *OrderService) Create(ctx context.Context, in CreateOrderInput) (*OrderView, error) {
	printer, err := svc.Printers.Get(ctx, in.PrinterID)
	if err != nil {
		return nil, err
	}
	if printer.Status == "offline" {
		return nil, domain.ErrPrinterOffline
	}

	if err := svc.Printers.EnsureOnline(printer); err != nil {
		return nil, err
	}

	user, err := svc.Q.GetUserByID(ctx, in.UserID)
	if err != nil {
		return nil, domain.ErrInternal.WithErr(err)
	}
	freeAccount := user.FreePrint != 0

	if max := svc.Runtime().MaxPendingOrdersPerUser; max > 0 {
		n, err := svc.Q.CountUserPendingOrders(ctx, in.UserID)
		if err != nil {
			return nil, domain.ErrInternal.WithErr(err)
		}
		if int(n) >= max {
			return nil, domain.ErrTooManyPendingOrders
		}
	}

	file, err := svc.Q.GetPrintFileForUser(ctx, store.GetPrintFileForUserParams{
		ID:     in.FileID,
		UserID: in.UserID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrFileNotFound
		}
		return nil, domain.ErrInternal.WithErr(err)
	}
	if file.PageCount <= 0 {
		return nil, domain.ErrValidation.WithMessage("该文件页数未知，请重新上传并指定页数")
	}

	params := in.Params.Normalize()
	if err := params.Validate(); err != nil {
		return nil, err
	}
	if err := svc.Printers.EnsureCapabilities(printer, params); err != nil {
		return nil, err
	}

	cost, err := domain.Quote(domain.QuoteInput{
		Params:              params,
		PageCount:           int(file.PageCount),
		PriceSingleBWFen:    printer.PriceSingleBwFen,
		PriceSingleColorFen: printer.PriceSingleColorFen,
		PriceDuplexBWFen:    printer.PriceDuplexBwFen,
		PriceDuplexColorFen: printer.PriceDuplexColorFen,
	})
	if err != nil {
		return nil, err
	}

	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, domain.ErrInternal.WithErr(err)
	}
	costJSON, err := json.Marshal(cost)
	if err != nil {
		return nil, domain.ErrInternal.WithErr(err)
	}

	now := svc.now()
	title := in.Title
	if title == "" {
		title = file.OriginalName
	}

	chargeFen := cost.TotalFen
	unpaid := chargeFen > 0 && !freeAccount
	if !unpaid {
		chargeFen = 0
	}
	freeOrder := !unpaid

	var created store.Order
	err = svc.InTx(ctx, func(q *store.Queries) error {

		if unpaid {
			dup, err := q.CountPendingOrdersByAmount(ctx, store.CountPendingOrdersByAmountParams{
				PrinterID: printer.ID,
				AmountFen: chargeFen,
			})
			if err != nil {
				return domain.ErrInternal.WithErr(err)
			}
			if dup > 0 {
				return domain.ErrAmountDuplicate
			}
		}

		orderNo, err := ids.NewOrderNo(now)
		if err != nil {
			return domain.ErrInternal.WithErr(err)
		}

		order, err := q.CreateOrder(ctx, store.CreateOrderParams{
			ID:            ids.New(),
			OrderNo:       orderNo,
			UserID:        in.UserID,
			PrinterID:     printer.ID,
			FileID:        file.ID,
			Status:        string(domain.OrderAwaitingPayment),
			AmountFen:     chargeFen,
			Currency:      cost.Currency,
			Title:         title,
			ParamsJson:    string(paramsJSON),
			CostJson:      string(costJSON),
			SidesPerCopy:  cost.SidesPerCopy,
			SheetsPerCopy: cost.SheetsPerCopy,
			IsTest:        boolToInt(in.IsTest),
			CreatedAt:     now.Unix(),
			UpdatedAt:     now.Unix(),
			ExpiresAt:     now.Add(svc.Runtime().OrderTTL).Unix(),
		})
		if err != nil {
			return domain.ErrInternal.WithErr(err)
		}
		created = order

		if err := svc.Audits.writeOrderAudit(ctx, q, orderAuditInput{
			Order:             order,
			Username:          user.Username,
			DeviceLabel:       in.DeviceLabel,
			DeviceFingerprint: in.DeviceFingerprint,
			FileName:          file.OriginalName,
			Params:            params,
			Cost:              cost,
		}); err != nil {
			return err
		}

		if !freeOrder {
			return nil
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
			return domain.ErrConflict.WithMessage("订单状态异常，请重试")
		}

		spent, err := q.SumSpentForUser(ctx, in.UserID)
		if err != nil {
			return domain.ErrInternal.WithErr(err)
		}
		if _, err := q.CreateLedgerEntry(ctx, store.CreateLedgerEntryParams{
			ID:            ids.New(),
			OrderID:       order.ID,
			UserID:        in.UserID,
			EntryType:     string(domain.LedgerPayment),
			AmountFen:     0,
			SpentAfterFen: spent,
			Channel:       string(domain.ChannelFree),
			Memo:          "免费打印，无需支付",
			CreatedAt:     now.Unix(),
		}); err != nil {
			return domain.ErrInternal.WithErr(err)
		}

		created.Status = string(domain.OrderAwaitingRelease)
		created.PaidAt = now.Unix()
		return nil
	})
	if err != nil {
		return nil, err
	}

	view := NewOrderView(created).
		WithPrinter(printer).
		WithFile(file).
		WithJob(nil).
		WithTiming(svc.Runtime().OrderTTL, svc.Runtime().ReleaseTTL, now)
	return &view, nil
}

type ReleaseInput struct {
	UserID  string
	OrderID string
	IP      string
}

func (svc *OrderService) Release(ctx context.Context, in ReleaseInput) (*OrderView, error) {

	order, err := svc.Q.GetOrderForUser(ctx, store.GetOrderForUserParams{
		ID:     in.OrderID,
		UserID: in.UserID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrOrderNotFound
		}
		return nil, domain.ErrInternal.WithErr(err)
	}

	status := domain.OrderStatus(order.Status)

	if status.IsReleased() {
		svc.Log.InfoContext(ctx, "订单已放行，按幂等处理", "order_no", order.OrderNo)
		return svc.Detail(ctx, in.UserID, order.ID)
	}

	if !status.CanRelease() {
		return nil, domain.ErrOrderNotReleasable.WithMessage(fmt.Sprintf(
			"订单当前状态为「%s」，只有「待确认打印」的订单可以确认打印", status.Label()))
	}

	if err := svc.EnsureReleaseWindowOpen(ctx, order); err != nil {
		return nil, err
	}

	if err := svc.Printers.EnsurePrintable(ctx, order.PrinterID); err != nil {
		return nil, err
	}

	if err := svc.Payments.EnsurePaymentVerified(ctx, order); err != nil {
		return nil, err
	}

	now := svc.now()
	err = svc.InTx(ctx, func(q *store.Queries) error {

		if _, err := q.GetJobByOrderID(ctx, order.ID); err == nil {
			return errReleaseAlreadyDone
		} else if !errors.Is(err, sql.ErrNoRows) {
			return domain.ErrInternal.WithErr(err)
		}

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

			return errReleaseAlreadyDone
		}
		return nil
	})

	if err != nil {
		if errors.Is(err, errReleaseAlreadyDone) {

			return svc.Detail(ctx, in.UserID, order.ID)
		}
		return nil, err
	}

	svc.Log.InfoContext(ctx, "用户已确认打印，任务入队",
		"order_no", order.OrderNo, "printer_id", order.PrinterID)

	view, err := svc.Detail(ctx, in.UserID, order.ID)
	if err != nil {
		return nil, err
	}
	view.JustReleased = true
	return view, nil
}

var errReleaseAlreadyDone = errors.New("print job already exists")

func (svc *OrderService) Detail(ctx context.Context, userID, orderID string) (*OrderView, error) {
	order, err := svc.Q.GetOrderForUser(ctx, store.GetOrderForUserParams{ID: orderID, UserID: userID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrOrderNotFound
		}
		return nil, domain.ErrInternal.WithErr(err)
	}

	view := NewOrderView(order).WithTiming(svc.Runtime().OrderTTL, svc.Runtime().ReleaseTTL, svc.now())

	if printer, err := svc.Q.GetPrinterByID(ctx, order.PrinterID); err == nil {
		view = view.WithPrinter(printer)
	}
	if file, err := svc.Q.GetPrintFileForUser(ctx, store.GetPrintFileForUserParams{
		ID:     order.FileID,
		UserID: userID,
	}); err == nil {
		view = view.WithFile(file)
	}
	job, err := svc.Q.GetJobByOrderID(ctx, order.ID)
	switch {
	case err == nil:
		view = view.WithJob(&job)
	case errors.Is(err, sql.ErrNoRows):
		view = view.WithJob(nil)
	default:
		return nil, domain.ErrInternal.WithErr(err)
	}
	return &view, nil
}

func (svc *OrderService) List(ctx context.Context, userID string, limit, offset int) ([]OrderView, int64, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	rows, err := svc.Q.ListOrdersForUser(ctx, store.ListOrdersForUserParams{
		UserID: userID,
		Limit:  int64(limit),
		Offset: int64(offset),
	})
	if err != nil {
		return nil, 0, domain.ErrInternal.WithErr(err)
	}
	total, err := svc.Q.CountOrdersForUser(ctx, userID)
	if err != nil {
		return nil, 0, domain.ErrInternal.WithErr(err)
	}

	now := svc.now()
	views := make([]OrderView, 0, len(rows))
	for _, r := range rows {
		views = append(views, orderViewFromListRow(r, svc.Runtime().OrderTTL, svc.Runtime().ReleaseTTL, now))
	}
	return views, total, nil
}

func (svc *OrderService) Cancel(ctx context.Context, userID, orderID, reason, ip string) error {
	order, err := svc.Q.GetOrderForUser(ctx, store.GetOrderForUserParams{ID: orderID, UserID: userID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrOrderNotFound
		}
		return domain.ErrInternal.WithErr(err)
	}
	status := domain.OrderStatus(order.Status)

	if status != domain.OrderAwaitingPayment {
		return domain.ErrOrderNotPayable.WithMessage(fmt.Sprintf(
			"「%s」的订单无法在这里取消；如需退款请联系管理员", status.Label()))
	}
	if reason == "" {
		reason = "用户主动取消"
	}

	now := svc.nowUnix()
	affected, err := svc.Q.CloseOrder(ctx, store.CloseOrderParams{
		Status:      string(domain.OrderCancelled),
		ClosedAt:    now,
		CloseReason: reason,
		UpdatedAt:   now,
		ID:          order.ID,
	})
	if err != nil {
		return domain.ErrInternal.WithErr(err)
	}
	if affected == 0 {

		return domain.ErrOrderNotPayable
	}

	return nil
}

func (svc *OrderService) ExpireStaleOrders(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	now := svc.nowUnix()
	rows, err := svc.Q.ListExpiredOrders(ctx, store.ListExpiredOrdersParams{
		ExpiresAt: now,
		Limit:     int64(limit),
	})
	if err != nil {
		return 0, domain.ErrInternal.WithErr(err)
	}

	closed := 0
	for _, order := range rows {
		affected, err := svc.Q.CloseOrder(ctx, store.CloseOrderParams{
			Status:      string(domain.OrderExpired),
			ClosedAt:    now,
			CloseReason: "超过支付时限",
			UpdatedAt:   now,
			ID:          order.ID,
		})
		if err != nil {
			svc.Log.WarnContext(ctx, "关闭超时订单失败", "order_id", order.ID, "error", err)
			continue
		}
		closed += int(affected)
	}
	return closed, nil
}

func (svc *OrderService) ExpireStaleReleases(ctx context.Context, limit int) (int, error) {
	ttl := svc.Runtime().ReleaseTTL
	if ttl <= 0 {
		return 0, nil
	}
	if limit <= 0 {
		limit = 100
	}

	deadline := svc.now().Add(-ttl).Unix()
	rows, err := svc.Q.ListStaleReleaseOrders(ctx, store.ListStaleReleaseOrdersParams{
		PaidAt: deadline,
		Limit:  int64(limit),
	})
	if err != nil {
		return 0, domain.ErrInternal.WithErr(err)
	}

	closed := 0
	for _, order := range rows {
		if err := svc.closeUnreleasedOrder(ctx, order,
			fmt.Sprintf("支付后超过 %s 未确认打印", ttl)); err != nil {
			svc.Log.WarnContext(ctx, "回收待确认订单失败", "order_id", order.ID, "error", err)
			continue
		}
		closed++
	}
	return closed, nil
}

func orderViewFromListRow(r store.ListOrdersForUserRow, orderTTL, releaseTTL time.Duration, now time.Time) OrderView {
	status := domain.OrderStatus(r.Status)
	view := OrderView{
		ID:              r.ID,
		OrderNo:         r.OrderNo,
		Status:          r.Status,
		StatusText:      status.Label(),
		Tone:            string(status.Tone()),
		AmountFen:       r.AmountFen,
		AmountText:      domain.FormatYuan(r.AmountFen),
		Currency:        r.Currency,
		Title:           r.Title,
		IsFree:          r.AmountFen == 0,
		PrinterCode:     r.PrinterCode,
		PrinterName:     r.PrinterName,
		PrinterLocation: r.PrinterLocation,
		FileName:        r.FileName,
		CreatedAt:       r.CreatedAt,
		ExpiresAt:       r.ExpiresAt,
		PaidAt:          r.PaidAt,
		CompletedAt:     r.CompletedAt,
		CloseReason:     r.CloseReason,
		CanPay:          status.CanPay(),
		CanCancel:       status.CanPay(),
		CanRelease:      status.CanRelease(),
	}

	if r.JobStatus != nil {
		js := domain.JobStatus(*r.JobStatus)
		view.JobStatus = *r.JobStatus
		view.JobStatusText = js.Label()
		view.JobTone = string(js.Tone())
	}
	if r.JobState != nil {
		view.JobState = *r.JobState
	}
	if r.JobLastError != nil {
		view.JobError = *r.JobLastError
	}
	if r.JobAttempts != nil {
		view.JobAttempts = int(*r.JobAttempts)
	}
	if r.CupsJobID != nil {
		view.CupsJobID = *r.CupsJobID
	}

	return view.WithTiming(orderTTL, releaseTTL, now)
}
