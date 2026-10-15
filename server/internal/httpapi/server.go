package httpapi

import (
	"bytes"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Sparkzure/PayPrint/server/internal/config"
	"github.com/Sparkzure/PayPrint/server/internal/service"
	"github.com/Sparkzure/PayPrint/server/internal/webui"
)

var distFiles = http.FileServerFS(webui.Dist)

type Server struct {
	svc *service.Services
	cfg *config.Config
	log *slog.Logger

	OnShutdown func(reason string)
}

func NewServer(svc *service.Services, cfg *config.Config, log *slog.Logger) *Server {
	return &Server{svc: svc, cfg: cfg, log: log}
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(recoverer(s.log))
	r.Use(requestLogger(s.log))
	r.Use(cors(s.cfg.CORSOrigins))

	r.Get("/healthz", s.handleHealth)

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/meta", s.handleMeta)

		r.Post("/auth/register", s.handleRegister)
		r.Post("/auth/login", s.handleLogin)

		r.Post("/collector/payment/report", s.handleCollectorPaymentReport)

		r.Group(func(r chi.Router) {
			r.Use(s.requireSession)
			r.Use(s.requireConsumer)

			r.Get("/me", s.handleMe)
			r.Get("/ledger", s.handleLedger)
			r.Get("/devices", s.handleListDevices)
			r.Get("/printers", s.handleListPrinters)
			r.Get("/printers/{printerID}/status", s.handlePrinterStatus)
			r.Get("/files", s.handleListFiles)

			r.Get("/files/{fileID}/content", s.handleFileContent)

			r.Post("/files", s.handleUploadFile)
			r.Get("/orders", s.handleListOrders)
			r.Get("/orders/{orderID}", s.handleOrderDetail)

			r.Post("/auth/logout", s.handleLogout)
			r.Post("/orders", s.handleCreateOrder)
			r.Post("/orders/{orderID}/cancel", s.handleCancelOrder)

			r.Post("/orders/{orderID}/print", s.handleReleaseOrder)

			r.Post("/payment/report", s.handlePaymentReport)
		})

		r.Route("/admin", func(r chi.Router) {

			r.Post("/login", s.handleAdminLogin)

			r.Group(func(r chi.Router) {
				r.Use(s.requireAdmin)

				r.Get("/me", s.handleAdminMe)
				r.Post("/logout", s.handleAdminLogout)

				r.Get("/orders", s.handleAdminListOrders)
				r.Post("/orders/{orderID}/retry", s.handleAdminRetryJob)
				r.Post("/orders/{orderID}/refund", s.handleAdminRefund)

				r.Get("/payment-events", s.handleAdminListPaymentEvents)

				r.Get("/audits", s.handleAdminListAudits)
				r.Get("/audits/export", s.handleAdminExportAudits)

				r.Get("/users", s.handleAdminListUsers)
				r.Post("/users", s.handleAdminCreateUser)

				r.Post("/users/{userID}/ban", s.handleAdminBanUser)
				r.Post("/users/{userID}/unban", s.handleAdminUnbanUser)

				r.Post("/users/{userID}/free-print", s.handleAdminSetUserFreePrint)

				r.Get("/devices", s.handleAdminListDevices)

				r.Post("/devices/{deviceID}/release", s.handleAdminReleaseDevice)
				r.Post("/devices/{deviceID}/block", s.handleAdminBlockDevice)

				r.Post("/devices/{deviceID}/restore", s.handleAdminRestoreDevice)

				r.Get("/collectors", s.handleAdminListCollectors)
				r.Post("/collectors", s.handleAdminIssueCollector)
				r.Delete("/collectors/{collectorID}", s.handleAdminRevokeCollector)
				r.Delete("/collectors/{collectorID}/purge", s.handleAdminPurgeCollector)

				r.Get("/printers", s.handleAdminListPrinters)
				r.Post("/printers", s.handleAdminUpsertPrinter)
				r.Post("/printers/{printerID}/status", s.handleAdminSetPrinterStatus)

				r.Delete("/printers/{printerID}", s.handleAdminDeletePrinter)

				r.Post("/printers/probe", s.handleAdminProbePrinter)

				r.Get("/settings", s.handleAdminSettings)
				r.Put("/settings", s.handleAdminUpdateSettings)

				r.Get("/stats", s.handleAdminStats)

				r.Get("/backup", s.handleAdminBackup)
				r.Get("/restore/pending", s.handleAdminStagedRestore)
				r.Post("/restore", s.handleAdminRestore)
				r.Post("/restore/cancel", s.handleAdminClearStagedRestore)
			})
		})
	})

	r.NotFound(s.serveSPA)

	return r
}

func (s *Server) serveSPA(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}

	clean := path.Clean("/" + r.URL.Path)
	if strings.HasPrefix(clean, "/api/") || clean == "/healthz" {
		http.NotFound(w, r)
		return
	}

	name := strings.TrimPrefix(clean, "/")
	if name != "" {
		if info, err := fs.Stat(webui.Dist, name); err == nil && !info.IsDir() {

			if strings.HasPrefix(name, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			distFiles.ServeHTTP(w, r)
			return
		}
	}

	index, err := fs.ReadFile(webui.Dist, "index.html")
	if err != nil {

		http.Error(w, "这个二进制里没有前端产物：构建时请先用 scripts/web.sh（或 scripts/web.ps1）下载前端产物，再重新构建",
			http.StatusNotFound)
		return
	}

	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
}
