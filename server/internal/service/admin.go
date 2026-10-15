package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/Sparkzure/PayPrint/server/internal/domain"
	"github.com/Sparkzure/PayPrint/server/internal/ids"
	"github.com/Sparkzure/PayPrint/server/internal/store"
)

type RefundInput struct {
	OrderID string
	ActorID string
	Reason  string
	IP      string
}

func (svc *PaymentService) Refund(ctx context.Context, in RefundInput) error {
	order, err := svc.Q.GetOrderByID(ctx, in.OrderID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrOrderNotFound
		}
		return domain.ErrInternal.WithErr(err)
	}
	if order.AmountFen <= 0 {
		return domain.ErrConflict.WithMessage("免费订单无需退款")
	}

	status := domain.OrderStatus(order.Status)
	switch status {
	case domain.OrderRefunded:
		return domain.ErrConflict.WithMessage("该订单已退款")
	case domain.OrderAwaitingRelease, domain.OrderQueued, domain.OrderPrinting,
		domain.OrderCompleted, domain.OrderRefundPending, domain.OrderDisputed,
		domain.OrderFailed:

	default:
		return domain.ErrConflict.WithMessage("订单当前状态不可退款：" + status.Label())
	}

	now := svc.nowUnix()
	reason := in.Reason
	if reason == "" {
		reason = "运维人工退款"
	}

	return svc.InTx(ctx, func(q *store.Queries) error {

		stopped, err := svc.stopPendingJob(ctx, q, order.ID, string(domain.JobCancelled),
			"订单已退款，取消投递")
		if err != nil {
			return err
		}
		if !stopped {

			svc.Log.ErrorContext(ctx, "退款时打印任务已投递、无法收回，纸可能已经出来",
				"order_no", order.OrderNo, "order_status", order.Status)
		}

		spent, err := q.SumSpentForUser(ctx, order.UserID)
		if err != nil {
			return domain.ErrInternal.WithErr(err)
		}
		spent -= order.AmountFen

		if _, err := q.CreateLedgerEntry(ctx, store.CreateLedgerEntryParams{
			ID:            ids.New(),
			OrderID:       order.ID,
			UserID:        order.UserID,
			EntryType:     string(domain.LedgerRefund),
			AmountFen:     -order.AmountFen,
			SpentAfterFen: spent,
			Channel:       "manual",
			Memo:          fmt.Sprintf("%s 退款：%s", order.OrderNo, reason),
			CreatedAt:     now,
		}); err != nil {

			if isUniqueViolation(err) {
				return domain.ErrConflict.WithMessage("该订单已退款")
			}
			return domain.ErrInternal.WithErr(err)
		}

		if err := q.UpdateOrderStatus(ctx, store.UpdateOrderStatusParams{
			Status:    string(domain.OrderRefunded),
			UpdatedAt: now,
			ID:        order.ID,
		}); err != nil {
			return domain.ErrInternal.WithErr(err)
		}

		return nil
	})
}

func (svc *OrderService) ListRecent(ctx context.Context, limit int) ([]OrderView, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := svc.Q.ListRecentOrders(ctx, int64(limit))
	if err != nil {
		return nil, domain.ErrInternal.WithErr(err)
	}

	printers := map[string]store.Printer{}
	if list, err := svc.Q.ListAllPrinters(ctx); err == nil {
		for _, p := range list {
			printers[p.ID] = p
		}
	} else {
		svc.Log.WarnContext(ctx, "查询打印机目录失败，订单打印机信息留空", "error", err)
	}

	owners := make(map[string]string)
	for _, u := range svc.userIndex(ctx) {
		owners[u.ID] = u.Username
	}

	now := svc.now()
	views := make([]OrderView, 0, len(rows))
	for _, o := range rows {
		view := NewOrderView(o).WithTiming(svc.Runtime().OrderTTL, svc.Runtime().ReleaseTTL, now)
		if p, ok := printers[o.PrinterID]; ok {
			view = view.WithPrinter(p)
		}
		view.UserID = o.UserID
		view.Username = owners[o.UserID]
		views = append(views, view)
	}
	return views, nil
}

func (svc *DeviceService) ListAll(ctx context.Context, limit int) ([]DeviceView, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := svc.Q.ListAllDevices(ctx, int64(limit))
	if err != nil {
		return nil, domain.ErrInternal.WithErr(err)
	}

	owners := svc.userIndex(ctx)

	views := make([]DeviceView, 0, len(rows))
	for _, d := range rows {
		view := NewDeviceView(d, "")
		if owner, ok := owners[view.UserID]; ok {
			view = view.WithOwner(owner)
		}
		views = append(views, view)
	}
	return views, nil
}

func (svc *Services) userIndex(ctx context.Context) map[string]store.User {
	users, err := svc.Q.ListUsers(ctx, 1000)
	if err != nil {
		svc.Log.WarnContext(ctx, "查询账号列表失败，归属信息留空", "error", err)
		return nil
	}
	owners := make(map[string]store.User, len(users))
	for _, u := range users {
		owners[u.ID] = u
	}
	return owners
}

func (svc *AuthService) ListUsers(ctx context.Context, limit int) ([]UserView, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := svc.Q.ListUsers(ctx, int64(limit))
	if err != nil {
		return nil, domain.ErrInternal.WithErr(err)
	}
	spentRows, err := svc.Q.ListSpentByUser(ctx)
	if err != nil {
		return nil, domain.ErrInternal.WithErr(err)
	}
	spentByUser := make(map[string]int64, len(spentRows))
	for _, row := range spentRows {
		spentByUser[row.UserID] = row.SpentFen
	}
	views := make([]UserView, 0, len(rows))
	for _, u := range rows {
		view := NewUserView(u)
		view.TotalSpentFen = spentByUser[u.ID]
		views = append(views, view)
	}
	return views, nil
}

func (svc *AuthService) UserCounts(ctx context.Context) (map[string]int64, error) {
	total, err := svc.Q.CountUsers(ctx)
	if err != nil {
		return nil, domain.ErrInternal.WithErr(err)
	}
	admins, err := svc.Q.CountUsersByRole(ctx, string(domain.RoleAdmin))
	if err != nil {
		return nil, domain.ErrInternal.WithErr(err)
	}
	return map[string]int64{
		"total": total,
		"admin": admins,
		"user":  total - admins,
	}, nil
}

type BanUserInput struct {
	UserID string

	Reason  string
	ActorID string
	IP      string
}

func (svc *AuthService) AdminBanUser(ctx context.Context, in BanUserInput) error {
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return domain.ErrValidation.WithMessage("封禁必须填写理由")
	}

	user, err := svc.Q.GetUserByID(ctx, in.UserID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrUserNotFound
		}
		return domain.ErrInternal.WithErr(err)
	}
	if user.Status == string(domain.UserStatusBanned) {
		return domain.ErrConflict.WithMessage("该账号已被封禁")
	}
	if in.ActorID != "" && in.ActorID != "admin-token" && in.ActorID == user.ID {
		return domain.ErrConflict.WithMessage("不能封禁当前登录的管理员账号")
	}
	if domain.UserRole(user.Role).IsAdmin() {
		active, err := svc.Q.CountActiveUsersByRole(ctx, string(domain.RoleAdmin))
		if err != nil {
			return domain.ErrInternal.WithErr(err)
		}
		if active <= 1 {
			return domain.ErrConflict.WithMessage(
				"这是最后一个启用中的管理员，封禁后管理面板将无法登录")
		}
	}

	now := svc.nowUnix()
	operator := svc.adminActorLabel(ctx, in.ActorID)
	return svc.InTx(ctx, func(q *store.Queries) error {
		if err := q.BanUser(ctx, store.BanUserParams{
			BanReason: reason,
			BannedAt:  now,
			BannedBy:  operator,
			UpdatedAt: now,
			ID:        user.ID,
		}); err != nil {
			return domain.ErrInternal.WithErr(err)
		}

		return nil
	})
}

func (svc *AuthService) AdminUnbanUser(ctx context.Context, in BanUserInput) error {
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return domain.ErrValidation.WithMessage("解封必须填写理由")
	}

	user, err := svc.Q.GetUserByID(ctx, in.UserID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrUserNotFound
		}
		return domain.ErrInternal.WithErr(err)
	}
	if user.Status != string(domain.UserStatusBanned) {
		return domain.ErrConflict.WithMessage("该账号当前未被封禁")
	}

	now := svc.nowUnix()
	return svc.InTx(ctx, func(q *store.Queries) error {
		if err := q.UnbanUser(ctx, store.UnbanUserParams{
			UpdatedAt: now,
			ID:        user.ID,
		}); err != nil {
			return domain.ErrInternal.WithErr(err)
		}

		svc.Log.InfoContext(ctx, "解封账号",
			"username", user.Username, "reason", reason, "actor", in.ActorID, "ip", in.IP)
		return nil
	})
}

type SetFreePrintInput struct {
	UserID  string
	Enabled bool

	Reason  string
	ActorID string
	IP      string
}

func (svc *AuthService) AdminSetUserFreePrint(
	ctx context.Context, in SetFreePrintInput,
) (*UserView, error) {
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return nil, domain.ErrValidation.WithMessage("修改免费打印必须填写理由")
	}

	user, err := svc.Q.GetUserByID(ctx, in.UserID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrUserNotFound
		}
		return nil, domain.ErrInternal.WithErr(err)
	}

	now := svc.nowUnix()
	var view UserView
	err = svc.InTx(ctx, func(q *store.Queries) error {
		if err := q.SetUserFreePrint(ctx, store.SetUserFreePrintParams{
			FreePrint: boolToInt(in.Enabled),
			UpdatedAt: now,
			ID:        user.ID,
		}); err != nil {
			return domain.ErrInternal.WithErr(err)
		}
		updated, err := q.GetUserByID(ctx, user.ID)
		if err != nil {
			return domain.ErrInternal.WithErr(err)
		}
		view = NewUserView(updated)
		spent, err := q.SumSpentForUser(ctx, updated.ID)
		if err != nil {
			return domain.ErrInternal.WithErr(err)
		}
		view.TotalSpentFen = spent

		state := "关闭"
		if in.Enabled {
			state = "开启"
		}

		svc.Log.WarnContext(ctx, "账号免费打印开关变更",
			"username", user.Username, "state", state, "reason", reason,
			"actor", in.ActorID, "ip", in.IP)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &view, nil
}

func (svc *AuthService) adminActorLabel(ctx context.Context, actorID string) string {
	actorID = strings.TrimSpace(actorID)
	if actorID == "" {
		return "unknown"
	}
	if actorID != "admin-token" {
		if user, err := svc.Q.GetUserByID(ctx, actorID); err == nil {
			return user.Username
		}
	}
	return actorID
}

func (svc *PrinterService) AdminProbeURI(
	ctx context.Context, cupsURI, printerID string,
) (*PrinterLiveView, error) {
	if value := strings.TrimSpace(cupsURI); value != "" {
		if !strings.HasPrefix(value, "ipp://") && !strings.HasPrefix(value, "ipps://") {
			return nil, domain.ErrValidation.WithMessage("cups_uri 必须以 ipp:// 或 ipps:// 开头")
		}
		return svc.probe(ctx, store.Printer{CupsUri: value})
	}

	printer, err := svc.Q.GetPrinterByID(ctx, strings.TrimSpace(printerID))
	if err != nil {
		return nil, domain.ErrPrinterNotFound
	}
	return svc.probe(ctx, printer)
}

func (svc *PrinterService) ListAll(ctx context.Context) ([]PrinterView, error) {
	rows, err := svc.Q.ListAllPrinters(ctx)
	if err != nil {
		return nil, domain.ErrInternal.WithErr(err)
	}
	views := make([]PrinterView, 0, len(rows))
	for _, p := range rows {
		view := svc.buildView(p)

		view.CupsURI = p.CupsUri
		views = append(views, view)
	}
	return views, nil
}

func (svc *DeviceService) AdminReleaseDevice(ctx context.Context, in ReleaseDeviceInput) error {
	device, err := svc.Q.GetDeviceByID(ctx, in.DeviceID)
	if err != nil {
		return domain.ErrDeviceNotFound
	}
	if domain.DeviceStatus(device.Status) == domain.DeviceReleased {
		return domain.ErrConflict.WithMessage("该设备已彻底解绑，无需重复操作")
	}

	now := svc.nowUnix()
	reason := in.Reason
	if reason == "" {
		reason = "运维人工解绑"
	}

	return svc.InTx(ctx, func(q *store.Queries) error {
		if _, err := q.DeleteSessionsByDevice(ctx, &device.ID); err != nil {
			return domain.ErrInternal.WithErr(err)
		}

		if _, err := q.DeleteNoncesByFingerprint(ctx, device.Fingerprint); err != nil {
			return domain.ErrInternal.WithErr(err)
		}
		affected, err := q.ReleaseDevice(ctx, store.ReleaseDeviceParams{
			RevokedAt:    now,
			RevokeReason: reason,
			UpdatedAt:    now,
			ID:           device.ID,
		})
		if err != nil {
			return domain.ErrInternal.WithErr(err)
		}
		if affected == 0 {

			return domain.ErrConflict.WithMessage("该设备已彻底解绑，无需重复操作")
		}

		svc.Log.WarnContext(ctx, "设备已彻底解绑",
			"device_id", device.ID, "fingerprint", device.Fingerprint,
			"reason", reason, "actor", in.ActorID, "ip", in.IP)
		return nil
	})
}

func (svc *DeviceService) BlockDevice(ctx context.Context, deviceID, actorID, reason, ip string) error {
	device, err := svc.Q.GetDeviceByID(ctx, deviceID)
	if err != nil {
		return domain.ErrDeviceNotFound
	}
	switch domain.DeviceStatus(device.Status) {
	case domain.DeviceRevoked:
		return domain.ErrConflict.WithMessage("该设备已处于停用状态")
	case domain.DeviceReleased:

		return domain.ErrConflict.WithMessage("该设备已彻底解绑，无法再停用")
	}
	now := svc.nowUnix()
	if reason == "" {
		reason = "运维停用"
	}

	return svc.InTx(ctx, func(q *store.Queries) error {
		affected, err := q.BlockDevice(ctx, store.BlockDeviceParams{
			RevokedAt:    now,
			RevokeReason: reason,
			UpdatedAt:    now,
			ID:           deviceID,
		})
		if err != nil {
			return domain.ErrInternal.WithErr(err)
		}
		if affected == 0 {

			return domain.ErrConflict.WithMessage("设备状态刚刚变化，请刷新后再试")
		}
		if _, err := q.RevokeSessionsByDevice(ctx, store.RevokeSessionsByDeviceParams{
			RevokedAt: now,
			DeviceID:  &deviceID,
		}); err != nil {
			return domain.ErrInternal.WithErr(err)
		}
		svc.Log.WarnContext(ctx, "设备已停用",
			"device_id", deviceID, "reason", reason, "actor", actorID, "ip", ip)
		return nil
	})
}

func (svc *DeviceService) AdminRestoreDevice(
	ctx context.Context, deviceID, actorID, reason, ip string,
) error {
	device, err := svc.Q.GetDeviceByID(ctx, deviceID)
	if err != nil {
		return domain.ErrDeviceNotFound
	}
	if device.Status != string(domain.DeviceRevoked) {
		return domain.ErrConflict.WithMessage("只有已停用的设备可以恢复")
	}

	bound := device.UserID != nil && *device.UserID != ""
	target := domain.DeviceUnbound
	if bound {
		target = domain.DeviceActive
	}
	if reason == "" {
		reason = "运维恢复"
	}

	now := svc.nowUnix()
	return svc.InTx(ctx, func(q *store.Queries) error {
		affected, err := q.RestoreDevice(ctx, store.RestoreDeviceParams{
			Status:    string(target),
			UpdatedAt: now,
			ID:        deviceID,
		})
		if err != nil {
			return domain.ErrInternal.WithErr(err)
		}
		if affected == 0 {
			return domain.ErrConflict.WithMessage("设备状态刚刚变化，请刷新后再试")
		}

		svc.Log.InfoContext(ctx, "设备已恢复",
			"device_id", deviceID, "target_status", target, "reason", reason,
			"actor", actorID, "ip", ip)
		return nil
	})
}

func (svc *PaymentService) TotalSpent(ctx context.Context, userID string) (int64, error) {
	spent, err := svc.Q.SumSpentForUser(ctx, userID)
	if err != nil {
		return 0, domain.ErrInternal.WithErr(err)
	}
	return spent, nil
}

func (svc *PaymentService) LedgerSnapshot(ctx context.Context, userID string, limit int) (int64, []LedgerView, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	total, err := svc.TotalSpent(ctx, userID)
	if err != nil {
		return 0, nil, err
	}
	rows, err := svc.Q.ListLedgerForUser(ctx, store.ListLedgerForUserParams{
		UserID: userID,
		Limit:  int64(limit),
	})
	if err != nil {
		return 0, nil, domain.ErrInternal.WithErr(err)
	}
	views := make([]LedgerView, 0, len(rows))
	for _, r := range rows {
		views = append(views, NewLedgerView(r))
	}
	return total, views, nil
}

func (svc *PaymentService) ListPaymentEvents(
	ctx context.Context, status string, limit, offset int,
) ([]PaymentEventView, error) {
	switch domain.PaymentEventStatus(status) {
	case "", domain.PaymentAccepted, domain.PaymentRejected,
		domain.PaymentDuplicate, domain.PaymentUnresolved:
	default:
		return nil, domain.ErrValidation.WithMessage(
			"status 只能是 accepted / rejected / duplicate / unresolved")
	}

	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	var (
		rows []store.PaymentEvent
		err  error
	)
	if status == "" {
		rows, err = svc.Q.ListRecentPaymentEvents(ctx, store.ListRecentPaymentEventsParams{
			Limit:  int64(limit),
			Offset: int64(offset),
		})
	} else {
		rows, err = svc.Q.ListPaymentEventsByStatus(ctx, store.ListPaymentEventsByStatusParams{
			Status: status,
			Limit:  int64(limit),
			Offset: int64(offset),
		})
	}
	if err != nil {
		return nil, domain.ErrInternal.WithErr(err)
	}

	owners := svc.userIndex(ctx)

	views := make([]PaymentEventView, 0, len(rows))
	for _, r := range rows {
		view := NewPaymentEventView(r)
		if owner, ok := owners[view.UserID]; ok {
			view = view.WithOwner(owner.Username)
		}
		views = append(views, view)
	}
	return views, nil
}

func (svc *JobService) JobStats(ctx context.Context) (map[string]int64, error) {
	rows, err := svc.Q.CountJobsByStatus(ctx)
	if err != nil {
		return nil, domain.ErrInternal.WithErr(err)
	}
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		out[r.Status] = r.Total
	}
	return out, nil
}
