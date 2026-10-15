package httpapi

import (
	"strconv"
	"strings"

	"net/http"

	"github.com/Sparkzure/PayPrint/server/internal/domain"
	"github.com/Sparkzure/PayPrint/server/internal/service"
)

type createOrderRequest struct {
	FileID    string             `json:"file_id"`
	PrinterID string             `json:"printer_id"`
	Title     string             `json:"title"`
	IsTest    bool               `json:"is_test"`
	Params    domain.PrintParams `json:"params"`
}

func (s *Server) handleCreateOrder(w http.ResponseWriter, r *http.Request) {
	session := sessionFrom(r.Context())
	if session == nil {
		writeError(w, r, s.log, domain.ErrUnauthorized)
		return
	}

	var req createOrderRequest
	if err := decodeJSON(w, r, &req, maxBodyBytes); err != nil {
		writeError(w, r, s.log, err)
		return
	}

	var deviceLabel, deviceFingerprint string
	if session.Device != nil {
		deviceLabel = session.Device.Label
		deviceFingerprint = session.Device.Fingerprint
	}

	view, err := s.svc.Orders.Create(r.Context(), service.CreateOrderInput{
		UserID:            session.User.ID,
		DeviceLabel:       deviceLabel,
		DeviceFingerprint: deviceFingerprint,
		FileID:            req.FileID,
		PrinterID:         req.PrinterID,
		Params:            req.Params,
		Title:             req.Title,
		IsTest:            req.IsTest,
		IP:                clientIP(r),
	})
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeData(w, http.StatusCreated, view)
}

func (s *Server) handleListOrders(w http.ResponseWriter, r *http.Request) {
	session := sessionFrom(r.Context())
	if session == nil {
		writeError(w, r, s.log, domain.ErrUnauthorized)
		return
	}
	limit := queryInt(r, "limit", 20)
	offset := queryInt(r, "offset", 0)

	views, total, err := s.svc.Orders.List(r.Context(), session.User.ID, limit, offset)
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeList(w, views, total, limit, offset)
}

func (s *Server) handleOrderDetail(w http.ResponseWriter, r *http.Request) {
	session := sessionFrom(r.Context())
	if session == nil {
		writeError(w, r, s.log, domain.ErrUnauthorized)
		return
	}
	orderID := paramString(r, "orderID")
	if orderID == "" {
		writeError(w, r, s.log, domain.ErrValidation.WithMessage("缺少订单 ID"))
		return
	}

	view, err := s.svc.Orders.Detail(r.Context(), session.User.ID, orderID)
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeData(w, http.StatusOK, view)
}

func (s *Server) handleCancelOrder(w http.ResponseWriter, r *http.Request) {
	session := sessionFrom(r.Context())
	if session == nil {
		writeError(w, r, s.log, domain.ErrUnauthorized)
		return
	}
	orderID := paramString(r, "orderID")
	if orderID == "" {
		writeError(w, r, s.log, domain.ErrValidation.WithMessage("缺少订单 ID"))
		return
	}

	if err := s.svc.Orders.Cancel(r.Context(), session.User.ID, orderID, "用户主动取消", clientIP(r)); err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleReleaseOrder(w http.ResponseWriter, r *http.Request) {
	session := sessionFrom(r.Context())
	if session == nil {
		writeError(w, r, s.log, domain.ErrUnauthorized)
		return
	}

	orderID := paramString(r, "orderID")
	if orderID == "" {
		writeError(w, r, s.log, domain.ErrValidation.WithMessage("缺少订单 ID"))
		return
	}

	view, err := s.svc.Orders.Release(r.Context(), service.ReleaseInput{
		UserID:  session.User.ID,
		OrderID: orderID,
		IP:      clientIP(r),
	})
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeData(w, http.StatusOK, view)
}

func parseInt(raw string) (int, error) {
	return strconv.Atoi(strings.TrimSpace(raw))
}
