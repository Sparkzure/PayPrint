package httpapi

import (
	"net/http"
	"strings"

	"github.com/Sparkzure/PayPrint/server/internal/domain"
	"github.com/Sparkzure/PayPrint/server/internal/notifier"
	"github.com/Sparkzure/PayPrint/server/internal/service"
)

func (s *Server) handlePaymentReport(w http.ResponseWriter, r *http.Request) {
	session := sessionFrom(r.Context())
	if session == nil {
		writeError(w, r, s.log, domain.ErrUnauthorized)
		return
	}

	source := strings.TrimSpace(r.URL.Query().Get("source"))
	if source == "" {
		source = strings.TrimSpace(r.Header.Get("X-Payment-Source"))
	}
	if source == "" {
		source = notifier.SourceWeChatAndroid
	}

	body, err := readBodyLimited(w, r, maxBodyBytes)
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}

	report, err := s.svc.Triggers.Parse(source, body)
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}

	result, err := s.svc.Payments.Report(r.Context(), service.ReportInput{
		Report: report,
		UserID: session.User.ID,
		IP:     clientIP(r),
	})
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}

	status := http.StatusOK
	if !result.Accepted {
		status = http.StatusConflict
	}
	writeData(w, status, result)
}

func (s *Server) handleCollectorPaymentReport(w http.ResponseWriter, r *http.Request) {
	body, err := readBodyLimited(w, r, maxBodyBytes)
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}

	identity, err := s.svc.Collectors.Authenticate(
		r.Context(), r.Header.Get(headerCollectorToken))
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}

	source := strings.TrimSpace(r.URL.Query().Get("source"))
	if source == "" {
		source = strings.TrimSpace(r.Header.Get("X-Payment-Source"))
	}
	if source == "" {
		source = notifier.SourceCollectorAPI
	}

	report, err := s.svc.Triggers.Parse(source, body)
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}

	result, err := s.svc.Payments.Report(r.Context(), service.ReportInput{
		Report:             report,
		CollectorPrinterID: identity.PrinterID,
		IP:                 clientIP(r),
	})
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}

	status := http.StatusOK
	if !result.Accepted {
		status = http.StatusConflict
	}
	writeData(w, status, result)
}
