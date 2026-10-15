package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/Sparkzure/PayPrint/server/internal/config"
	"github.com/Sparkzure/PayPrint/server/internal/database"
	"github.com/Sparkzure/PayPrint/server/internal/domain"
	"github.com/Sparkzure/PayPrint/server/internal/ipp"
	"github.com/Sparkzure/PayPrint/server/internal/notifier"
	"github.com/Sparkzure/PayPrint/server/internal/store"
)

type PrinterProber interface {
	GetPrinter(ctx context.Context, printerURI string) (*ipp.PrinterInfo, error)
}

type Deps struct {
	DB       *sql.DB
	Q        *store.Queries
	Cfg      *config.Config
	IPP      *ipp.Client
	Triggers *notifier.Registry
	Queriers *notifier.Queriers
	Log      *slog.Logger

	Prober PrinterProber

	Now func() time.Time
}

type Services struct {
	*Deps

	Auth    *AuthService
	Devices *DeviceService
	Files   *FileService
	Orders  *OrderService

	Audits   *AuditService
	Payments *PaymentService
	Printers *PrinterService
	Jobs     *JobService

	Collectors *CollectorService

	Settings *SettingsService

	settings settingsStore
}

func New(d *Deps) *Services {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Prober == nil && d.IPP != nil {
		d.Prober = d.IPP
	}
	s := &Services{Deps: d}

	initial := defaultRuntime()
	s.settings.ptr.Store(&initial)

	s.Auth = &AuthService{s}
	s.Devices = &DeviceService{s}
	s.Files = &FileService{s}
	s.Orders = &OrderService{s}
	s.Audits = &AuditService{s}
	s.Payments = &PaymentService{s}
	s.Printers = &PrinterService{s}
	s.Jobs = &JobService{s}
	s.Collectors = &CollectorService{s}
	s.Settings = &SettingsService{s}
	return s
}

func (s *Services) now() time.Time { return s.Now() }

func (s *Services) MigrationVersion(_ context.Context) (int64, error) {
	v, err := database.MigrationVersion(s.DB)
	if err != nil {
		return 0, domain.ErrInternal.WithErr(err)
	}
	return v, nil
}

func (s *Services) nowUnix() int64 { return s.Now().Unix() }

func (s *Services) InTx(ctx context.Context, fn func(q *store.Queries) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return domain.ErrInternal.WithErr(fmt.Errorf("开启事务: %w", err))
	}
	defer func() {

		_ = tx.Rollback()
	}()

	if err := fn(s.Q.WithTx(tx)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return domain.ErrInternal.WithErr(fmt.Errorf("提交事务: %w", err))
	}
	return nil
}
