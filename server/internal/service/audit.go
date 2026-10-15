package service

import (
	"context"
	"fmt"

	"github.com/Sparkzure/PayPrint/server/internal/domain"
	"github.com/Sparkzure/PayPrint/server/internal/ids"
	"github.com/Sparkzure/PayPrint/server/internal/store"
)

type AuditService struct{ *Services }

type orderAuditInput struct {
	Order    store.Order
	Username string

	DeviceLabel       string
	DeviceFingerprint string
	FileName          string
	Params            domain.PrintParams
	Cost              domain.CostBreakdown
}

func (svc *AuditService) writeOrderAudit(
	ctx context.Context, q *store.Queries, in orderAuditInput,
) error {

	totalPages := in.Cost.SidesPerCopy * int64(in.Params.Copies)
	if totalPages < 0 {
		totalPages = 0
	}
	if err := q.CreateOrderAudit(ctx, store.CreateOrderAuditParams{
		ID:                ids.New(),
		OrderID:           in.Order.ID,
		OrderNo:           in.Order.OrderNo,
		UserID:            in.Order.UserID,
		Username:          in.Username,
		DeviceLabel:       in.DeviceLabel,
		DeviceFingerprint: in.DeviceFingerprint,
		AmountFen:         in.Order.AmountFen,
		FileName:          in.FileName,
		ColorMode:         string(in.Params.ColorMode),
		Duplex:            string(in.Params.Duplex),
		Copies:            int64(in.Params.Copies),
		TotalPages:        totalPages,
		CreatedAt:         svc.nowUnix(),
	}); err != nil {
		return domain.ErrInternal.WithErr(fmt.Errorf("写入订单审计: %w", err))
	}
	return nil
}

func (svc *AuditService) List(ctx context.Context, limit, offset int) ([]OrderAuditView, int64, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}

	rows, err := svc.Q.ListOrderAudits(ctx, store.ListOrderAuditsParams{
		Limit:  int64(limit),
		Offset: int64(offset),
	})
	if err != nil {
		return nil, 0, domain.ErrInternal.WithErr(err)
	}
	total, err := svc.Q.CountOrderAudits(ctx)
	if err != nil {
		return nil, 0, domain.ErrInternal.WithErr(err)
	}

	views := make([]OrderAuditView, 0, len(rows))
	for _, row := range rows {
		views = append(views, NewOrderAuditView(row))
	}
	return views, total, nil
}
