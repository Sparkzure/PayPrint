package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Sparkzure/PayPrint/server/internal/domain"
	"github.com/Sparkzure/PayPrint/server/internal/ids"
	"github.com/Sparkzure/PayPrint/server/internal/ipp"
	"github.com/Sparkzure/PayPrint/server/internal/store"
)

type PrinterService struct{ *Services }

func (svc *PrinterService) List(ctx context.Context, withLive bool) ([]PrinterView, error) {
	rows, err := svc.Q.ListPrinters(ctx)
	if err != nil {
		return nil, domain.ErrInternal.WithErr(err)
	}
	views := make([]PrinterView, 0, len(rows))
	for _, p := range rows {
		view := svc.buildView(p)
		if withLive {
			live, _ := svc.probe(ctx, p)
			view.Live = live
		}
		views = append(views, view)
	}
	return views, nil
}

func (svc *PrinterService) Get(ctx context.Context, printerID string) (store.Printer, error) {
	p, err := svc.Q.GetPrinterByID(ctx, printerID)
	if err != nil {
		return store.Printer{}, domain.ErrPrinterNotFound
	}
	if p.Status == "disabled" {
		return store.Printer{}, domain.ErrPrinterNotFound
	}
	return p, nil
}

func (svc *PrinterService) Probe(ctx context.Context, printerID string) (*PrinterLiveView, error) {
	p, err := svc.Get(ctx, printerID)
	if err != nil {
		return nil, err
	}
	return svc.probe(ctx, p)
}

func (svc *PrinterService) EnsureCapabilities(p store.Printer, params domain.PrintParams) error {
	sizes := decodeStringList(p.PaperSizes)
	if len(sizes) == 0 {

		return domain.ErrParamUnsupported.WithMessage(
			fmt.Sprintf("%s 尚未配置可用纸张，请联系管理员在管理面板设置", p.Name))
	}
	if !contains(sizes, string(params.PaperSize)) {
		return domain.ErrParamUnsupported.WithMessage(
			fmt.Sprintf("%s 不支持 %s 纸张", p.Name, params.PaperSize))
	}
	if params.ColorMode == domain.ColorModeColor && p.SupportsColor == 0 {
		return domain.ErrParamUnsupported.WithMessage(p.Name + " 不支持彩色打印")
	}
	if params.Duplex != domain.DuplexOneSided && p.SupportsDuplex == 0 {
		return domain.ErrParamUnsupported.WithMessage(p.Name + " 不支持双面打印")
	}
	if p.MaxCopies > 0 && int64(params.Copies) > p.MaxCopies {
		return domain.ErrParamUnsupported.WithMessage(
			fmt.Sprintf("%s 单次最多打印 %d 份", p.Name, p.MaxCopies))
	}
	return nil
}

func (svc *PrinterService) EnsurePrintable(ctx context.Context, printerID string) error {
	printer, err := svc.Q.GetPrinterByID(ctx, printerID)
	if err != nil {
		return domain.ErrPrinterNotFound
	}

	switch printer.Status {
	case "offline":
		return domain.ErrPrinterOffline.WithMessage(
			"打印机「" + printer.Name + "」处于离线状态，请联系管理员")
	case "maintenance":
		return domain.ErrPrinterOffline.WithMessage(
			"打印机「" + printer.Name + "」正在维护，请稍后重试")
	case "disabled":
		return domain.ErrPrinterNotFound
	}

	if err := svc.EnsureOnline(printer); err != nil {
		return err
	}

	prober := svc.prober()
	if prober == nil {

		return domain.ErrPrinterOffline.WithMessage("服务未配置打印机探测能力，无法确认打印点可用")
	}
	info, err := prober.GetPrinter(ctx, printer.CupsUri)
	if err != nil {
		return domain.ErrPrinterOffline.WithMessage(
			"无法连接打印机「" + printer.Name + "」，请确认打印点已开机后重试").WithErr(err)
	}
	if !info.AcceptingJobs {
		return domain.ErrPrinterOffline.WithMessage(
			"打印机「" + printer.Name + "」暂不接受新任务，请稍后重试")
	}
	if ipp.PrinterStateCode(info.State) == ipp.PrinterStateStopped {
		return domain.ErrPrinterOffline.WithMessage(
			"打印机「" + printer.Name + "」已暂停，请通知管理员")
	}
	return nil
}

func (svc *PrinterService) Bootstrap(ctx context.Context) error {
	existing, err := svc.Q.ListAllPrinters(ctx)
	if err != nil {
		return domain.ErrInternal.WithErr(err)
	}
	if len(existing) > 0 {
		return nil
	}

	now := svc.nowUnix()
	if _, err := svc.Q.UpsertPrinter(ctx, store.UpsertPrinterParams{
		ID:                  ids.New(),
		Code:                "P01",
		Name:                svc.Cfg.DefaultPrinterName,
		CupsUri:             svc.Cfg.DefaultPrinterURI,
		Location:            "自助打印点",
		Status:              "offline",
		PriceSingleBwFen:    100,
		PriceSingleColorFen: 300,
		PriceDuplexBwFen:    200,
		PriceDuplexColorFen: 600,
		SupportsColor:       1,
		SupportsDuplex:      1,
		PaperSizes:          `["A4","A3"]`,
		MaxCopies:           99,
		SortOrder:           0,

		OnlineWindows: "[]",
		CreatedAt:     now,
		UpdatedAt:     now,
	}); err != nil {
		return domain.ErrInternal.WithErr(err)
	}

	svc.Log.WarnContext(ctx, "已登记默认打印机，但它的地址还没验证过",
		"code", "P01", "uri", svc.Cfg.DefaultPrinterURI,
		"hint", "在管理面板的「打印机」页改地址并点「测试连接」，确认后再上线")
	return nil
}

type UpsertPrinterInput struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	CupsURI  string `json:"cups_uri"`
	Location string `json:"location"`
	Status   string `json:"status"`

	PriceSingleBWFen    int64    `json:"price_single_bw_fen"`
	PriceSingleColorFen int64    `json:"price_single_color_fen"`
	PriceDuplexBWFen    int64    `json:"price_duplex_bw_fen"`
	PriceDuplexColorFen int64    `json:"price_duplex_color_fen"`
	SupportsColor       bool     `json:"supports_color"`
	SupportsDuplex      bool     `json:"supports_duplex"`
	PaperSizes          []string `json:"paper_sizes"`
	MaxCopies           int      `json:"max_copies"`
	SortOrder           int      `json:"sort_order"`

	OnlineWindows []OnlineWindow `json:"online_windows"`
}

func (svc *PrinterService) Upsert(ctx context.Context, in UpsertPrinterInput) (*PrinterView, error) {
	if strings.TrimSpace(in.Code) == "" || strings.TrimSpace(in.Name) == "" {
		return nil, domain.ErrValidation.WithMessage("code 与 name 必填")
	}
	if !strings.HasPrefix(in.CupsURI, "ipp://") && !strings.HasPrefix(in.CupsURI, "ipps://") {
		return nil, domain.ErrValidation.WithMessage("cups_uri 必须以 ipp:// 或 ipps:// 开头")
	}

	if in.PriceSingleBWFen < 0 || in.PriceSingleColorFen < 0 ||
		in.PriceDuplexBWFen < 0 || in.PriceDuplexColorFen < 0 {
		return nil, domain.ErrValidation.WithMessage("打印单价不能为负")
	}

	if err := ValidateOnlineWindows(in.OnlineWindows); err != nil {
		return nil, err
	}
	status := in.Status
	if status == "" {
		status = "online"
	}
	sizes := in.PaperSizes
	if len(sizes) == 0 {
		sizes = []string{"A4"}
	}
	maxCopies := in.MaxCopies
	if maxCopies <= 0 {
		maxCopies = 99
	}
	encoded, err := json.Marshal(sizes)
	if err != nil {
		return nil, domain.ErrInternal.WithErr(err)
	}
	windows, err := EncodeOnlineWindows(in.OnlineWindows)
	if err != nil {
		return nil, domain.ErrInternal.WithErr(err)
	}

	now := svc.nowUnix()
	row, err := svc.Q.UpsertPrinter(ctx, store.UpsertPrinterParams{
		ID:                  ids.New(),
		Code:                strings.TrimSpace(in.Code),
		Name:                strings.TrimSpace(in.Name),
		CupsUri:             strings.TrimSpace(in.CupsURI),
		Location:            in.Location,
		Status:              status,
		PriceSingleBwFen:    in.PriceSingleBWFen,
		PriceSingleColorFen: in.PriceSingleColorFen,
		PriceDuplexBwFen:    in.PriceDuplexBWFen,
		PriceDuplexColorFen: in.PriceDuplexColorFen,
		SupportsColor:       boolToInt(in.SupportsColor),
		SupportsDuplex:      boolToInt(in.SupportsDuplex),
		PaperSizes:          string(encoded),
		MaxCopies:           int64(maxCopies),
		SortOrder:           int64(in.SortOrder),
		OnlineWindows:       windows,
		CreatedAt:           now,
		UpdatedAt:           now,
	})
	if err != nil {
		return nil, domain.ErrInternal.WithErr(err)
	}

	view := svc.buildView(row)
	return &view, nil
}

func (svc *PrinterService) SetStatus(ctx context.Context, printerID, status string) error {
	switch status {
	case "online", "offline", "maintenance", "disabled":
	default:
		return domain.ErrValidation.WithMessage("不支持的打印机状态")
	}
	if err := svc.Q.SetPrinterStatus(ctx, store.SetPrinterStatusParams{
		Status:    status,
		UpdatedAt: svc.nowUnix(),
		ID:        printerID,
	}); err != nil {
		return domain.ErrInternal.WithErr(err)
	}
	return nil
}

type DeletePrinterResult struct {
	Code           string `json:"code"`
	Name           string `json:"name"`
	Orders         int64  `json:"deleted_orders"`
	PrintJobs      int64  `json:"deleted_print_jobs"`
	LedgerEntries  int64  `json:"deleted_ledger_entries"`
	PaymentEvents  int64  `json:"deleted_payment_events"`
	Verifications  int64  `json:"deleted_order_verifications"`
	CollectorCreds int64  `json:"deleted_collector_credentials"`
}

func (svc *PrinterService) Delete(ctx context.Context, printerID, reason, actorID, ip string) (*DeletePrinterResult, error) {
	printer, err := svc.Q.GetPrinterByID(ctx, printerID)
	if err != nil {
		return nil, domain.ErrPrinterNotFound
	}

	res := &DeletePrinterResult{Code: printer.Code, Name: printer.Name}
	err = svc.InTx(ctx, func(q *store.Queries) error {
		steps := []struct {
			dst *int64
			run func() (int64, error)
		}{
			{&res.PrintJobs, func() (int64, error) { return q.DeletePrintJobsByPrinter(ctx, printerID) }},
			{&res.LedgerEntries, func() (int64, error) { return q.DeleteLedgerEntriesByPrinter(ctx, printerID) }},
			{&res.PaymentEvents, func() (int64, error) { return q.DeletePaymentEventsByPrinter(ctx, printerID) }},
			{&res.Verifications, func() (int64, error) { return q.DeleteOrderVerificationsByPrinter(ctx, printerID) }},
			{&res.Orders, func() (int64, error) { return q.DeleteOrdersByPrinter(ctx, printerID) }},
			{&res.CollectorCreds, func() (int64, error) { return q.DeleteCollectorCredentialsByPrinter(ctx, printerID) }},
		}
		for _, step := range steps {
			n, err := step.run()
			if err != nil {
				return domain.ErrInternal.WithErr(err)
			}
			*step.dst = n
		}

		affected, err := q.DeletePrinter(ctx, printerID)
		if err != nil {
			return domain.ErrInternal.WithErr(err)
		}
		if affected == 0 {
			return domain.ErrPrinterNotFound
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	svc.Log.WarnContext(ctx, "打印机已级联删除",
		"code", printer.Code, "name", printer.Name,
		"orders", res.Orders, "print_jobs", res.PrintJobs,
		"ledger_entries", res.LedgerEntries, "payment_events", res.PaymentEvents,
		"collector_credentials", res.CollectorCreds,
		"reason", strings.TrimSpace(reason), "actor", actorID, "ip", ip)
	return res, nil
}

func (svc *PrinterService) buildView(p store.Printer) PrinterView {
	statusText := "在线"
	tone := domain.ToneSuccess
	switch p.Status {
	case "offline":
		statusText = "离线"
		tone = domain.ToneDanger
	case "maintenance":
		statusText = "维护中"
		tone = domain.ToneWarning
	case "disabled":
		statusText = "已停用"
		tone = domain.ToneNeutral
	}
	view := PrinterView{
		ID:                   p.ID,
		Code:                 p.Code,
		Name:                 p.Name,
		Location:             p.Location,
		Status:               p.Status,
		StatusText:           statusText,
		Tone:                 string(tone),
		PriceSingleBWFen:     p.PriceSingleBwFen,
		PriceSingleColorFen:  p.PriceSingleColorFen,
		PriceDuplexBWFen:     p.PriceDuplexBwFen,
		PriceDuplexColorFen:  p.PriceDuplexColorFen,
		PriceSingleBWText:    domain.FormatYuan(p.PriceSingleBwFen),
		PriceSingleColorText: domain.FormatYuan(p.PriceSingleColorFen),
		PriceDuplexBWText:    domain.FormatYuan(p.PriceDuplexBwFen),
		PriceDuplexColorText: domain.FormatYuan(p.PriceDuplexColorFen),
		SupportsColor:        p.SupportsColor != 0,
		SupportsDuplex:       p.SupportsDuplex != 0,
		PaperSizes:           decodeStringList(p.PaperSizes),
		MaxCopies:            int(p.MaxCopies),
	}

	wins := ParseOnlineWindows(p.OnlineWindows)
	view.OnlineWindows = wins
	view.OnlineWindowsText = DescribeOnlineWindows(wins)
	view.WithinSchedule = InOnlineWindow(wins, svc.now())
	return view
}

func (svc *Services) prober() PrinterProber {
	if svc.Prober != nil {
		return svc.Prober
	}
	if svc.IPP != nil {
		return svc.IPP
	}
	return nil
}

func (svc *PrinterService) probe(ctx context.Context, p store.Printer) (*PrinterLiveView, error) {
	prober := svc.prober()
	if prober == nil {
		return &PrinterLiveView{Reachable: false, Error: "服务未配置打印机探测能力"}, nil
	}
	info, err := prober.GetPrinter(ctx, p.CupsUri)
	if err != nil {

		return &PrinterLiveView{Reachable: false, Error: err.Error()}, nil
	}

	state := info.State
	if state == "" {
		state = "unknown"
	}
	return &PrinterLiveView{
		Reachable:     true,
		State:         state,
		StateReasons:  info.StateReasons,
		StateMessage:  info.StateMessage,
		AcceptingJobs: info.AcceptingJobs,
		MakeAndModel:  info.MakeAndModel,
	}, nil
}

func decodeStringList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
