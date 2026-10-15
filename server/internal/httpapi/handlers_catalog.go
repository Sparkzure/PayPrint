package httpapi

import (
	"net/http"
	"time"

	"github.com/Sparkzure/PayPrint/server/internal/database"
	"github.com/Sparkzure/PayPrint/server/internal/domain"
	"github.com/Sparkzure/PayPrint/server/internal/service"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	type health struct {
		Status    string `json:"status"`
		Database  string `json:"database"`
		Migration int64  `json:"migration_version"`
		Time      int64  `json:"time"`
	}

	out := health{Status: "ok", Database: "ok", Time: time.Now().Unix()}

	if err := s.svc.DB.PingContext(ctx); err != nil {
		out.Status = "degraded"
		out.Database = "error: " + err.Error()
		writeJSON(w, http.StatusServiceUnavailable, dataEnvelope{Data: out})
		return
	}
	if version, err := database.MigrationVersion(s.svc.DB); err == nil {
		out.Migration = version
	}
	writeData(w, http.StatusOK, out)
}

type metaResponse struct {
	App                   string         `json:"app"`
	RegistrationOpen      bool           `json:"registration_open"`
	SupportedPaperSizes   []string       `json:"supported_paper_sizes"`
	SupportedNUp          []int          `json:"supported_n_up"`
	SupportedDuplex       []paramOption  `json:"supported_duplex"`
	SupportedColorModes   []paramOption  `json:"supported_color_modes"`
	SupportedOrientations []paramOption  `json:"supported_orientations"`
	SupportedQualities    []paramOption  `json:"supported_qualities"`
	SupportedChannels     []string       `json:"supported_channels"`
	PaymentReportSources  []string       `json:"payment_report_sources"`
	OrderTTLSeconds       int64          `json:"order_ttl_seconds"`
	SignatureSkewSeconds  int64          `json:"signature_skew_seconds"`
	SigningScheme         string         `json:"signing_scheme"`
	UploadMaxBytes        int64          `json:"upload_max_bytes"`
	OrderStatusCatalog    []statusOption `json:"order_statuses"`
	MaxCopies             int            `json:"max_copies"`

	AutoReleaseOnPayment bool `json:"auto_release_on_payment"`

	RequirePaymentVerification bool `json:"require_payment_verification"`

	ReleaseTTLSeconds int64 `json:"release_ttl_seconds"`
}

type paramOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type statusOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Tone  string `json:"tone"`
}

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	sources := s.svc.Triggers.Names()

	runtime := s.svc.Runtime()

	writeData(w, http.StatusOK, metaResponse{
		App:                        "payprint",
		RegistrationOpen:           runtime.OpenRegistration,
		AutoReleaseOnPayment:       runtime.AutoReleaseOnPayment,
		RequirePaymentVerification: runtime.RequirePaymentVerification,
		ReleaseTTLSeconds:          int64(runtime.ReleaseTTL.Seconds()),
		SupportedPaperSizes: []string{
			string(domain.PaperA4), string(domain.PaperA5),
			string(domain.PaperA3), string(domain.PaperLetter),
		},
		SupportedNUp: domain.SupportedNUp,
		SupportedDuplex: []paramOption{
			{Value: string(domain.DuplexOneSided), Label: "单面"},
			{Value: string(domain.DuplexLongEdge), Label: "双面（长边翻页）"},
			{Value: string(domain.DuplexShortEdge), Label: "双面（短边翻页）"},
		},
		SupportedColorModes: []paramOption{
			{Value: string(domain.ColorModeMonochrome), Label: "黑白"},
			{Value: string(domain.ColorModeColor), Label: "彩色"},
		},
		SupportedOrientations: []paramOption{
			{Value: string(domain.OrientationPortrait), Label: "纵向"},
			{Value: string(domain.OrientationLandscape), Label: "横向"},
		},
		SupportedQualities: []paramOption{
			{Value: string(domain.QualityDraft), Label: "草稿"},
			{Value: string(domain.QualityNormal), Label: "标准"},
			{Value: string(domain.QualityHigh), Label: "高质量"},
		},
		SupportedChannels:    []string{string(domain.ChannelWeChat), string(domain.ChannelAlipay)},
		PaymentReportSources: sources,
		OrderTTLSeconds:      int64(runtime.OrderTTL.Seconds()),
		SignatureSkewSeconds: int64(s.cfg.SignatureSkew.Seconds()),
		SigningScheme:        "payprint/v1",
		UploadMaxBytes:       s.cfg.MaxUploadBytes,
		OrderStatusCatalog:   orderStatusCatalog(),
		MaxCopies:            999,
	})
}

func orderStatusCatalog() []statusOption {

	all := []domain.OrderStatus{
		domain.OrderAwaitingPayment,
		domain.OrderAwaitingRelease,
		domain.OrderQueued,
		domain.OrderPrinting,
		domain.OrderCompleted,
		domain.OrderFailed,
		domain.OrderRefundPending,
		domain.OrderDisputed,
		domain.OrderRefunded,
		domain.OrderExpired,
		domain.OrderCancelled,
	}
	out := make([]statusOption, 0, len(all))
	for _, st := range all {
		out = append(out, statusOption{
			Value: string(st),
			Label: st.Label(),
			Tone:  string(st.Tone()),
		})
	}
	return out
}

func (s *Server) handleListPrinters(w http.ResponseWriter, r *http.Request) {
	if sessionFrom(r.Context()) == nil {
		writeError(w, r, s.log, domain.ErrUnauthorized)
		return
	}
	withLive := queryBool(r, "live", false)
	views, err := s.svc.Printers.List(r.Context(), withLive)
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeList(w, views, int64(len(views)), len(views), 0)
}

func (s *Server) handlePrinterStatus(w http.ResponseWriter, r *http.Request) {
	if sessionFrom(r.Context()) == nil {
		writeError(w, r, s.log, domain.ErrUnauthorized)
		return
	}
	printerID := paramString(r, "printerID")
	if printerID == "" {
		writeError(w, r, s.log, domain.ErrValidation.WithMessage("缺少打印机 ID"))
		return
	}
	live, err := s.svc.Printers.Probe(r.Context(), printerID)
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeData(w, http.StatusOK, live)
}

func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	session := sessionFrom(r.Context())
	if session == nil {
		writeError(w, r, s.log, domain.ErrUnauthorized)
		return
	}
	limit := queryInt(r, "limit", 20)
	views, err := s.svc.Files.List(r.Context(), session.User.ID, int64(limit))
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeList(w, views, int64(len(views)), limit, 0)
}

func (s *Server) handleFileContent(w http.ResponseWriter, r *http.Request) {
	session := sessionFrom(r.Context())
	if session == nil {
		writeError(w, r, s.log, domain.ErrUnauthorized)
		return
	}
	fileID := paramString(r, "fileID")
	if fileID == "" {
		writeError(w, r, s.log, domain.ErrValidation.WithMessage("缺少文件 ID"))
		return
	}

	meta, err := s.svc.Files.GetForUser(r.Context(), session.User.ID, fileID)
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}

	f, err := s.svc.Files.Open(meta)
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		writeError(w, r, s.log, domain.ErrInternal.WithErr(err))
		return
	}

	contentType := meta.ContentType
	if contentType == "" {
		contentType = "application/pdf"
	}
	w.Header().Set("Content-Type", contentType)

	w.Header().Set("Content-Disposition", "inline; filename=\"preview.pdf\"")
	w.Header().Set("Cache-Control", "private, max-age=60")
	http.ServeContent(w, r, "preview.pdf", info.ModTime(), f)
}

func (s *Server) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	session := sessionFrom(r.Context())
	if session == nil {
		writeError(w, r, s.log, domain.ErrUnauthorized)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes+1<<20)

	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeError(w, r, s.log, domain.ErrValidation.WithMessage("解析上传表单失败（可能超过大小上限）"))
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, r, s.log, domain.ErrValidation.WithMessage("缺少 file 字段"))
		return
	}
	defer file.Close()

	pageHint := 0
	if raw := r.FormValue("page_count"); raw != "" {
		if v, err := parseInt(raw); err == nil {
			pageHint = v
		}
	}

	view, err := s.svc.Files.Upload(r.Context(), service.UploadInput{
		UserID:        session.User.ID,
		Filename:      header.Filename,
		ContentType:   header.Header.Get("Content-Type"),
		DeclaredSize:  header.Size,
		Reader:        file,
		PageCountHint: pageHint,
	})
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeData(w, http.StatusCreated, view)
}
