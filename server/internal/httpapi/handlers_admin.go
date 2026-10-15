package httpapi

import (
	"net/http"

	"github.com/Sparkzure/PayPrint/server/internal/domain"
	"github.com/Sparkzure/PayPrint/server/internal/service"
)

func (s *Server) handleAdminListOrders(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 50)
	views, err := s.svc.Orders.ListRecent(r.Context(), limit)
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeList(w, views, int64(len(views)), limit, 0)
}

func (s *Server) handleAdminListDevices(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 100)
	views, err := s.svc.Devices.ListAll(r.Context(), limit)
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeList(w, views, int64(len(views)), limit, 0)
}

type adminReasonRequest struct {
	Reason string `json:"reason"`
}

type adminCreateUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`

	Role string `json:"role"`
}

func (s *Server) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	var req adminCreateUserRequest
	if err := decodeJSON(w, r, &req, 64<<10); err != nil {
		writeError(w, r, s.log, err)
		return
	}

	view, err := s.svc.Auth.AdminCreateUser(r.Context(), service.CreateUserInput{
		Username: req.Username,
		Password: req.Password,
		Role:     req.Role,
		ActorID:  adminActorID(r.Context()),
		IP:       clientIP(r),
	})
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeData(w, http.StatusCreated, view)
}

func (s *Server) handleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 200)
	views, err := s.svc.Auth.ListUsers(r.Context(), limit)
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeList(w, views, int64(len(views)), limit, 0)
}

func (s *Server) handleAdminListPrinters(w http.ResponseWriter, r *http.Request) {
	views, err := s.svc.Printers.ListAll(r.Context())
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeList(w, views, int64(len(views)), len(views), 0)
}

type adminIssueCollectorRequest struct {
	PrinterCode string `json:"printer_code"`

	Label string `json:"label"`
}

func (s *Server) handleAdminIssueCollector(w http.ResponseWriter, r *http.Request) {
	var req adminIssueCollectorRequest
	if err := decodeJSON(w, r, &req, 64<<10); err != nil {
		writeError(w, r, s.log, err)
		return
	}
	view, err := s.svc.Collectors.Issue(r.Context(), service.IssueCollectorInput{
		PrinterCode: req.PrinterCode,
		Label:       req.Label,
		ActorID:     adminActorID(r.Context()),
		IP:          clientIP(r),
	})
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeData(w, http.StatusCreated, view)
}

func (s *Server) handleAdminListCollectors(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 100)
	views, err := s.svc.Collectors.List(r.Context(), limit)
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeList(w, views, int64(len(views)), limit, 0)
}

func (s *Server) handleAdminRevokeCollector(w http.ResponseWriter, r *http.Request) {
	collectorID := paramString(r, "collectorID")
	if collectorID == "" {
		writeError(w, r, s.log, domain.ErrValidation.WithMessage("缺少凭据 ID"))
		return
	}

	var req adminReasonRequest
	if r.ContentLength > 0 {
		if err := decodeJSON(w, r, &req, 64<<10); err != nil {
			writeError(w, r, s.log, err)
			return
		}
	}
	if err := s.svc.Collectors.Revoke(r.Context(), collectorID, req.Reason,
		adminActorID(r.Context()), clientIP(r)); err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAdminPurgeCollector(w http.ResponseWriter, r *http.Request) {
	collectorID := paramString(r, "collectorID")
	if collectorID == "" {
		writeError(w, r, s.log, domain.ErrValidation.WithMessage("缺少凭据 ID"))
		return
	}
	var req adminReasonRequest
	if r.ContentLength > 0 {
		if err := decodeJSON(w, r, &req, 64<<10); err != nil {
			writeError(w, r, s.log, err)
			return
		}
	}
	if err := s.svc.Collectors.Purge(r.Context(), collectorID, req.Reason,
		adminActorID(r.Context()), clientIP(r)); err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAdminReleaseDevice(w http.ResponseWriter, r *http.Request) {
	deviceID := paramString(r, "deviceID")
	if deviceID == "" {
		writeError(w, r, s.log, domain.ErrValidation.WithMessage("缺少设备 ID"))
		return
	}
	var req adminReasonRequest
	if err := decodeJSON(w, r, &req, 64<<10); err != nil {
		writeError(w, r, s.log, err)
		return
	}

	if err := s.svc.Devices.AdminReleaseDevice(r.Context(), service.ReleaseDeviceInput{
		DeviceID:  deviceID,
		Reason:    req.Reason,
		ActorType: "admin",
		ActorID:   adminActorID(r.Context()),
		IP:        clientIP(r),
	}); err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAdminBlockDevice(w http.ResponseWriter, r *http.Request) {
	deviceID := paramString(r, "deviceID")
	if deviceID == "" {
		writeError(w, r, s.log, domain.ErrValidation.WithMessage("缺少设备 ID"))
		return
	}
	var req adminReasonRequest
	if err := decodeJSON(w, r, &req, 64<<10); err != nil {
		writeError(w, r, s.log, err)
		return
	}

	if err := s.svc.Devices.BlockDevice(r.Context(), deviceID,
		adminActorID(r.Context()), req.Reason, clientIP(r)); err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAdminRestoreDevice(w http.ResponseWriter, r *http.Request) {
	deviceID := paramString(r, "deviceID")
	if deviceID == "" {
		writeError(w, r, s.log, domain.ErrValidation.WithMessage("缺少设备 ID"))
		return
	}
	var req adminReasonRequest
	if err := decodeJSON(w, r, &req, 64<<10); err != nil {
		writeError(w, r, s.log, err)
		return
	}

	if err := s.svc.Devices.AdminRestoreDevice(r.Context(), deviceID,
		adminActorID(r.Context()), req.Reason, clientIP(r)); err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAdminRetryJob(w http.ResponseWriter, r *http.Request) {
	orderID := paramString(r, "orderID")
	if orderID == "" {
		writeError(w, r, s.log, domain.ErrValidation.WithMessage("缺少订单 ID"))
		return
	}
	if err := s.svc.Jobs.RetryOrder(r.Context(), orderID, adminActorID(r.Context())); err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"ok": true})
}

type refundRequest struct {
	Reason string `json:"reason"`
}

func (s *Server) handleAdminRefund(w http.ResponseWriter, r *http.Request) {
	orderID := paramString(r, "orderID")
	if orderID == "" {
		writeError(w, r, s.log, domain.ErrValidation.WithMessage("缺少订单 ID"))
		return
	}
	var req refundRequest
	if err := decodeJSON(w, r, &req, 64<<10); err != nil {
		writeError(w, r, s.log, err)
		return
	}

	if err := s.svc.Payments.Refund(r.Context(), service.RefundInput{
		OrderID: orderID,
		ActorID: adminActorID(r.Context()),
		Reason:  req.Reason,
		IP:      clientIP(r),
	}); err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAdminUpsertPrinter(w http.ResponseWriter, r *http.Request) {
	var req service.UpsertPrinterInput
	if err := decodeJSON(w, r, &req, 256<<10); err != nil {
		writeError(w, r, s.log, err)
		return
	}
	view, err := s.svc.Printers.Upsert(r.Context(), req)
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeData(w, http.StatusOK, view)
}

type printerStatusRequest struct {
	Status string `json:"status"`
}

func (s *Server) handleAdminSetPrinterStatus(w http.ResponseWriter, r *http.Request) {
	printerID := paramString(r, "printerID")
	if printerID == "" {
		writeError(w, r, s.log, domain.ErrValidation.WithMessage("缺少打印机 ID"))
		return
	}
	var req printerStatusRequest
	if err := decodeJSON(w, r, &req, 64<<10); err != nil {
		writeError(w, r, s.log, err)
		return
	}
	if err := s.svc.Printers.SetStatus(r.Context(), printerID, req.Status); err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAdminDeletePrinter(w http.ResponseWriter, r *http.Request) {
	printerID := paramString(r, "printerID")
	if printerID == "" {
		writeError(w, r, s.log, domain.ErrValidation.WithMessage("缺少打印机 ID"))
		return
	}
	var req adminReasonRequest
	if r.ContentLength > 0 {
		if err := decodeJSON(w, r, &req, 64<<10); err != nil {
			writeError(w, r, s.log, err)
			return
		}
	}
	res, err := s.svc.Printers.Delete(r.Context(), printerID, req.Reason,
		adminActorID(r.Context()), clientIP(r))
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeData(w, http.StatusOK, res)
}

func (s *Server) handleAdminListPaymentEvents(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 50)
	offset := queryInt(r, "offset", 0)

	views, err := s.svc.Payments.ListPaymentEvents(
		r.Context(), queryString(r, "status"), limit, offset)
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeList(w, views, int64(len(views)), limit, offset)
}

func (s *Server) handleAdminStats(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.svc.Jobs.JobStats(r.Context())
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	users, err := s.svc.Auth.UserCounts(r.Context())
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}

	runtime := s.svc.Runtime()
	writeData(w, http.StatusOK, map[string]any{
		"print_jobs_by_status":         jobs,
		"users":                        users,
		"auto_release_on_payment":      runtime.AutoReleaseOnPayment,
		"require_payment_verification": runtime.RequirePaymentVerification,
		"release_ttl_seconds":          int64(runtime.ReleaseTTL.Seconds()),
		"open_registration":            runtime.OpenRegistration,
		"order_ttl_seconds":            int64(runtime.OrderTTL.Seconds()),
		"payment_sources":              s.svc.Triggers.Names(),
		"wechat_query_enabled":         s.cfg.WeChat.Enabled,

		"log_level":                s.cfg.LogLevel,
		"worker_enabled":           s.cfg.Worker.Enabled,
		"signature_skew_seconds":   int64(s.cfg.SignatureSkew.Seconds()),
		"nonce_ttl_seconds":        int64(s.cfg.NonceTTL.Seconds()),
		"max_upload_bytes":         s.cfg.MaxUploadBytes,
		"session_ttl_seconds":      int64(s.cfg.SessionTTL.Seconds()),
		"default_printer_uri_seed": s.cfg.DefaultPrinterURI,
	})
}

func (s *Server) handleAdminSettings(w http.ResponseWriter, r *http.Request) {
	views, err := s.svc.Settings.List(r.Context())
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeData(w, http.StatusOK, views)
}

type adminSettingsRequest struct {
	Set   map[string]string `json:"set"`
	Reset []string          `json:"reset"`
}

func (s *Server) handleAdminUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req adminSettingsRequest
	if err := decodeJSON(w, r, &req, 64<<10); err != nil {
		writeError(w, r, s.log, err)
		return
	}
	views, err := s.svc.Settings.Update(r.Context(), service.UpdateSettingsInput{
		Set:     req.Set,
		Reset:   req.Reset,
		ActorID: adminActorID(r.Context()),
		IP:      clientIP(r),
	})
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeData(w, http.StatusOK, views)
}

type adminProbeRequest struct {
	CupsURI   string `json:"cups_uri"`
	PrinterID string `json:"printer_id"`
}

func (s *Server) handleAdminProbePrinter(w http.ResponseWriter, r *http.Request) {
	var req adminProbeRequest
	if err := decodeJSON(w, r, &req, 64<<10); err != nil {
		writeError(w, r, s.log, err)
		return
	}
	view, err := s.svc.Printers.AdminProbeURI(r.Context(), req.CupsURI, req.PrinterID)
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeData(w, http.StatusOK, view)
}

type adminBanRequest struct {
	Reason string `json:"reason"`
}

type adminFreePrintRequest struct {
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason"`
}

func (s *Server) handleAdminSetUserFreePrint(w http.ResponseWriter, r *http.Request) {
	userID := paramString(r, "userID")
	if userID == "" {
		writeError(w, r, s.log, domain.ErrValidation.WithMessage("缺少账号 ID"))
		return
	}
	var req adminFreePrintRequest
	if err := decodeJSON(w, r, &req, 64<<10); err != nil {
		writeError(w, r, s.log, err)
		return
	}
	view, err := s.svc.Auth.AdminSetUserFreePrint(r.Context(), service.SetFreePrintInput{
		UserID:  userID,
		Enabled: req.Enabled,
		Reason:  req.Reason,
		ActorID: adminActorID(r.Context()),
		IP:      clientIP(r),
	})
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeData(w, http.StatusOK, view)
}

func (s *Server) handleAdminBanUser(w http.ResponseWriter, r *http.Request) {
	s.handleAdminUserBanSwitch(w, r, false)
}

func (s *Server) handleAdminUnbanUser(w http.ResponseWriter, r *http.Request) {
	s.handleAdminUserBanSwitch(w, r, true)
}

func (s *Server) handleAdminUserBanSwitch(w http.ResponseWriter, r *http.Request, unban bool) {
	userID := paramString(r, "userID")
	if userID == "" {
		writeError(w, r, s.log, domain.ErrValidation.WithMessage("缺少账号 ID"))
		return
	}
	var req adminBanRequest
	if err := decodeJSON(w, r, &req, 64<<10); err != nil {
		writeError(w, r, s.log, err)
		return
	}
	in := service.BanUserInput{
		UserID:  userID,
		Reason:  req.Reason,
		ActorID: adminActorID(r.Context()),
		IP:      clientIP(r),
	}
	var err error
	if unban {
		err = s.svc.Auth.AdminUnbanUser(r.Context(), in)
	} else {
		err = s.svc.Auth.AdminBanUser(r.Context(), in)
	}
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"ok": true})
}
