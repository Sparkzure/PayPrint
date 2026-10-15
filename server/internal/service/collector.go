package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/Sparkzure/PayPrint/server/internal/auth"
	"github.com/Sparkzure/PayPrint/server/internal/domain"
	"github.com/Sparkzure/PayPrint/server/internal/ids"
	"github.com/Sparkzure/PayPrint/server/internal/store"
)

type CollectorService struct{ *Services }

type IssueCollectorInput struct {
	PrinterCode string

	Label   string
	ActorID string
	IP      string
}

func (svc *CollectorService) Issue(ctx context.Context, in IssueCollectorInput) (*IssuedCollectorView, error) {
	code := strings.TrimSpace(in.PrinterCode)
	if code == "" {
		return nil, domain.ErrValidation.WithMessage("printer_code 不能为空（收款凭据必须归属某个打印机）")
	}
	printer, err := svc.Q.GetPrinterByCode(ctx, code)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrPrinterNotFound.WithMessage("打印机不存在，请先创建打印机")
		}
		return nil, domain.ErrInternal.WithErr(err)
	}

	token, hash, err := auth.NewAPIToken()
	if err != nil {
		return nil, domain.ErrInternal.WithErr(err)
	}

	label := strings.TrimSpace(in.Label)
	if label == "" {
		label = printer.Name + " 收款端"
	}

	now := svc.nowUnix()
	var view IssuedCollectorView
	err = svc.InTx(ctx, func(q *store.Queries) error {
		cred, err := q.CreateCollectorCredential(ctx, store.CreateCollectorCredentialParams{
			ID:        ids.New(),
			PrinterID: printer.ID,
			Label:     label,
			TokenHash: hash,
			CreatedAt: now,
		})
		if err != nil {
			return domain.ErrInternal.WithErr(err)
		}
		view = IssuedCollectorView{
			CollectorView: NewCollectorView(cred).WithPrinter(printer),
			Token:         token,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	svc.Log.InfoContext(ctx, "签发收款凭据",
		"printer", printer.Code, "label", label, "actor", in.ActorID, "ip", in.IP)
	return &view, nil
}

func (svc *CollectorService) List(ctx context.Context, limit int) ([]CollectorView, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := svc.Q.ListCollectorCredentials(ctx, int64(limit))
	if err != nil {
		return nil, domain.ErrInternal.WithErr(err)
	}

	printers, err := svc.Q.ListAllPrinters(ctx)
	if err != nil {
		return nil, domain.ErrInternal.WithErr(err)
	}
	byID := make(map[string]store.Printer, len(printers))
	for _, p := range printers {
		byID[p.ID] = p
	}

	views := make([]CollectorView, 0, len(rows))
	for _, c := range rows {
		view := NewCollectorView(c)
		if p, ok := byID[c.PrinterID]; ok {
			view = view.WithPrinter(p)
		}
		views = append(views, view)
	}
	return views, nil
}

func (svc *CollectorService) Revoke(ctx context.Context, id, reason, actorID, ip string) error {
	if strings.TrimSpace(id) == "" {
		return domain.ErrValidation.WithMessage("缺少凭据 ID")
	}
	now := svc.nowUnix()
	return svc.InTx(ctx, func(q *store.Queries) error {
		affected, err := q.RevokeCollectorCredential(ctx, store.RevokeCollectorCredentialParams{
			RevokedAt:    now,
			RevokeReason: strings.TrimSpace(reason),
			ID:           id,
		})
		if err != nil {
			return domain.ErrInternal.WithErr(err)
		}
		if affected == 0 {

			return domain.ErrCollectorCredentialNotFound.WithMessage("凭据不存在或此前已被吊销")
		}

		svc.Log.InfoContext(ctx, "吊销收款凭据", "collector_id", id, "actor", actorID, "ip", ip)
		return nil
	})
}

func (svc *CollectorService) Purge(ctx context.Context, id, reason, actorID, ip string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return domain.ErrValidation.WithMessage("缺少凭据 ID")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "管理面板删除"
	}
	err := svc.InTx(ctx, func(q *store.Queries) error {
		affected, err := q.DeleteCollectorCredential(ctx, id)
		if err != nil {
			return domain.ErrInternal.WithErr(err)
		}
		if affected == 0 {
			return domain.ErrCollectorCredentialNotFound.WithMessage("凭据不存在")
		}
		return nil
	})
	if err != nil {
		return err
	}
	svc.Log.WarnContext(ctx, "彻底删除收款凭据",
		"collector_id", id, "reason", reason, "actor", actorID, "ip", ip)
	return nil
}

type CollectorIdentity struct {
	Credential store.CollectorCredential

	PrinterID string
}

func (svc *CollectorService) Authenticate(ctx context.Context, token string) (*CollectorIdentity, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, domain.ErrCollectorCredentialInvalid
	}
	cred, err := svc.Q.GetCollectorCredentialByTokenHash(ctx, auth.HashToken(token))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrCollectorCredentialInvalid
		}
		return nil, domain.ErrInternal.WithErr(err)
	}
	if cred.RevokedAt != 0 {
		return nil, domain.ErrCollectorCredentialInvalid.WithMessage("收款凭据已被吊销")
	}

	if err := svc.Q.TouchCollectorCredential(ctx, store.TouchCollectorCredentialParams{
		LastUsedAt: svc.nowUnix(),
		ID:         cred.ID,
	}); err != nil {
		svc.Log.WarnContext(ctx, "刷新收款凭据活跃时间失败", "credential_id", cred.ID, "error", err)
	}

	return &CollectorIdentity{Credential: cred, PrinterID: cred.PrinterID}, nil
}
