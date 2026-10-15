package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Sparkzure/PayPrint/server/internal/domain"
	"github.com/Sparkzure/PayPrint/server/internal/store"
)

func (s *Services) stopPendingJob(
	ctx context.Context, q *store.Queries, orderID, status, reason string,
) (bool, error) {
	job, err := q.GetJobByOrderID(ctx, orderID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {

			return true, nil
		}
		return false, domain.ErrInternal.WithErr(err)
	}
	if job.Status != string(domain.JobPending) {
		return false, nil
	}

	now := s.nowUnix()
	affected, err := q.CancelPendingJob(ctx, store.CancelPendingJobParams{
		Status:     status,
		LastError:  truncate(reason, 512),
		FinishedAt: now,
		UpdatedAt:  now,
		OrderID:    orderID,
	})
	if err != nil {
		return false, domain.ErrInternal.WithErr(err)
	}
	return affected > 0, nil
}

func (s *Services) markOrderDisputed(ctx context.Context, order store.Order, reason string) {
	now := s.nowUnix()

	err := s.InTx(ctx, func(q *store.Queries) error {
		stopped, err := s.stopPendingJob(ctx, q, order.ID, string(domain.JobNeedsReview), reason)
		if err != nil {
			return err
		}
		if !stopped {
			s.Log.ErrorContext(ctx, "订单转入异常待核，但打印任务已投递、无法收回",
				"order_no", order.OrderNo, "reason", reason)
		}

		if err := q.UpdateOrderStatus(ctx, store.UpdateOrderStatusParams{
			Status:    string(domain.OrderDisputed),
			UpdatedAt: now,
			ID:        order.ID,
		}); err != nil {
			return domain.ErrInternal.WithErr(err)
		}

		return nil
	})
	if err != nil {
		s.Log.ErrorContext(ctx, "标记订单异常待核失败", "order_id", order.ID, "error", err)
		return
	}

	s.Log.ErrorContext(ctx, "订单转入异常待核", "order_no", order.OrderNo, "reason", reason)
}

func (s *Services) closeUnreleasedOrder(ctx context.Context, order store.Order, reason string) error {
	now := s.nowUnix()
	next := domain.OrderRefundPending
	if order.AmountFen <= 0 {
		next = domain.OrderExpired
	}

	affected, err := s.Q.CloseAwaitingReleaseOrder(ctx, store.CloseAwaitingReleaseOrderParams{
		Status:      string(next),
		ClosedAt:    now,
		CloseReason: reason,
		UpdatedAt:   now,
		ID:          order.ID,
	})
	if err != nil {
		return domain.ErrInternal.WithErr(err)
	}
	if affected == 0 {

		return domain.ErrOrderNotReleasable
	}

	s.Log.WarnContext(ctx, "待确认打印的订单已超时关闭",
		"order_no", order.OrderNo, "amount_fen", order.AmountFen, "next", next, "reason", reason)
	return nil
}

func (s *Services) EnsureReleaseWindowOpen(ctx context.Context, order store.Order) error {
	ttl := s.Runtime().ReleaseTTL
	if ttl <= 0 || order.PaidAt <= 0 {
		return nil
	}
	deadline := order.PaidAt + int64(ttl.Seconds())
	if s.nowUnix() <= deadline {
		return nil
	}

	reason := fmt.Sprintf("支付后超过 %s 未确认打印", ttl)
	if err := s.closeUnreleasedOrder(ctx, order, reason); err != nil {

		return domain.ErrReleaseWindowClosed.WithErr(err)
	}
	return domain.ErrReleaseWindowClosed
}
