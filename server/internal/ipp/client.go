package ipp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/OpenPrinting/goipp"

	"github.com/Sparkzure/PayPrint/server/internal/domain"
)

const defaultPort = "631"

const (
	statusClientErrorNotFound = 0x0406

	statusClientErrorGone = 0x0407

	statusClientErrorBadRequest = 0x0400

	statusServerErrorStart = 0x0500
)

type Error struct {
	Op        string
	Status    goipp.Status
	HTTPCode  int
	Transient bool
	Err       error
}

func (e *Error) Error() string {
	parts := []string{"IPP " + e.Op}
	if e.Status != 0 {
		parts = append(parts, e.Status.String())
	}
	if e.HTTPCode != 0 {
		parts = append(parts, fmt.Sprintf("http=%d", e.HTTPCode))
	}
	msg := strings.Join(parts, " ")
	if e.Err != nil {
		return msg + ": " + e.Err.Error()
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

func IsTransient(err error) bool {
	var ippErr *Error
	if errors.As(err, &ippErr) {
		return ippErr.Transient
	}

	return false
}

func IsJobNotFound(err error) bool {
	var ippErr *Error
	if !errors.As(err, &ippErr) {
		return false
	}
	switch int(ippErr.Status) {
	case statusClientErrorNotFound, statusClientErrorGone:
		return true
	default:
		return false
	}
}

type Client struct {
	http     *http.Client
	userName string
	seq      atomic.Uint32

	timeout atomic.Int64
}

func NewClient(timeout time.Duration, userName string) *Client {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	c := &Client{
		http:     &http.Client{Transport: transport},
		userName: userName,
	}
	c.timeout.Store(int64(timeout))
	return c
}

func (c *Client) SetTimeout(d time.Duration) {
	if d > 0 {
		c.timeout.Store(int64(d))
	}
}

type PrintRequest struct {
	PrinterURI     string
	JobName        string
	DocumentFormat string
	Params         domain.PrintParams

	PageRanges []domain.PageRange
	Document   io.Reader
}

type JobInfo struct {
	ID           int64
	URI          string
	Name         string
	State        string
	RawState     int64
	StateReasons []string
	StateMessage string
}

type PrinterInfo struct {
	State         string
	RawState      int64
	StateReasons  []string
	StateMessage  string
	AcceptingJobs bool
	MakeAndModel  string
}

func (c *Client) PrintJob(ctx context.Context, req PrintRequest) (*JobInfo, error) {
	if req.PrinterURI == "" {
		return nil, &Error{Op: "Print-Job", Err: errors.New("printer-uri 为空")}
	}
	format := req.DocumentFormat
	if format == "" {
		format = "application/pdf"
	}

	msg := goipp.NewRequest(goipp.DefaultVersion, goipp.OpPrintJob, c.nextRequestID())
	msg.Operation.Add(goipp.MakeAttribute("attributes-charset", goipp.TagCharset, goipp.String("utf-8")))
	msg.Operation.Add(goipp.MakeAttribute("attributes-natural-language", goipp.TagLanguage, goipp.String("en-us")))
	msg.Operation.Add(goipp.MakeAttribute("printer-uri", goipp.TagURI, goipp.String(req.PrinterURI)))
	msg.Operation.Add(goipp.MakeAttribute("requesting-user-name", goipp.TagName, goipp.String(c.userName)))
	msg.Operation.Add(goipp.MakeAttribute("job-name", goipp.TagName, goipp.String(req.JobName)))
	msg.Operation.Add(goipp.MakeAttribute("document-format", goipp.TagMimeType, goipp.String(format)))
	ApplyParams(&msg.Job, req.Params, req.PageRanges)

	payload, err := msg.EncodeBytes()
	if err != nil {
		return nil, &Error{Op: "Print-Job", Err: fmt.Errorf("编码 IPP 请求: %w", err)}
	}

	body := io.MultiReader(bytes.NewReader(payload), req.Document)
	rsp, err := c.do(ctx, "Print-Job", req.PrinterURI, body)
	if err != nil {
		return nil, err
	}

	info := jobInfoFromAttrs(rsp.Job)
	if info.URI == "" {

		info.URI = buildJobURI(req.PrinterURI, info.ID)
	}
	return &info, nil
}

func (c *Client) GetJob(ctx context.Context, printerURI, jobURI string, jobID int64) (*JobInfo, error) {
	var (
		lastErr  error
		notFound error
	)

	if strings.TrimSpace(jobURI) != "" {
		info, err := c.getJobAttributes(ctx, printerURI, jobURI, 0)
		if err == nil {
			return info, nil
		}
		if IsJobNotFound(err) {
			notFound = err
		}
		lastErr = err
	}

	if jobID > 0 {
		info, err := c.getJobAttributes(ctx, printerURI, "", jobID)
		if err == nil {
			return info, nil
		}
		if IsJobNotFound(err) && notFound == nil {
			notFound = err
		}
		lastErr = err
	}

	if notFound != nil {
		return nil, notFound
	}
	if lastErr == nil {
		return nil, &Error{Op: "Get-Job-Attributes", Err: errors.New("job-uri 与 job-id 都不可用")}
	}
	return nil, lastErr
}

func (c *Client) getJobAttributes(
	ctx context.Context, printerURI, jobURI string, jobID int64,
) (*JobInfo, error) {
	msg := goipp.NewRequest(goipp.DefaultVersion, goipp.OpGetJobAttributes, c.nextRequestID())
	msg.Operation.Add(goipp.MakeAttribute("attributes-charset", goipp.TagCharset, goipp.String("utf-8")))
	msg.Operation.Add(goipp.MakeAttribute("attributes-natural-language", goipp.TagLanguage, goipp.String("en-us")))
	if jobURI != "" {
		msg.Operation.Add(goipp.MakeAttribute("job-uri", goipp.TagURI, goipp.String(jobURI)))
	} else {
		msg.Operation.Add(goipp.MakeAttribute("printer-uri", goipp.TagURI, goipp.String(printerURI)))
		msg.Operation.Add(goipp.MakeAttribute("job-id", goipp.TagInteger, goipp.Integer(jobID)))
	}
	msg.Operation.Add(goipp.MakeAttribute("requesting-user-name", goipp.TagName, goipp.String(c.userName)))
	msg.Operation.Add(goipp.MakeAttr("requested-attributes", goipp.TagKeyword,
		goipp.String("job-id"),
		goipp.String("job-uri"),
		goipp.String("job-name"),
		goipp.String("job-state"),
		goipp.String("job-state-reasons"),
		goipp.String("job-state-message")))

	payload, err := msg.EncodeBytes()
	if err != nil {
		return nil, &Error{Op: "Get-Job-Attributes", Err: fmt.Errorf("编码 IPP 请求: %w", err)}
	}
	rsp, err := c.do(ctx, "Get-Job-Attributes", printerURI, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}

	info := jobInfoFromAttrs(rsp.Job)
	if info.URI == "" {
		switch {
		case jobID > 0:
			info.URI = buildJobURI(printerURI, jobID)
		default:
			info.URI = jobURI
		}
	}
	return &info, nil
}

func (c *Client) GetJobs(ctx context.Context, printerURI, whichJobs string) ([]JobInfo, error) {
	msg := goipp.NewRequest(goipp.DefaultVersion, goipp.OpGetJobs, c.nextRequestID())
	msg.Operation.Add(goipp.MakeAttribute("attributes-charset", goipp.TagCharset, goipp.String("utf-8")))
	msg.Operation.Add(goipp.MakeAttribute("attributes-natural-language", goipp.TagLanguage, goipp.String("en-us")))
	msg.Operation.Add(goipp.MakeAttribute("printer-uri", goipp.TagURI, goipp.String(printerURI)))
	if whichJobs != "" {
		msg.Operation.Add(goipp.MakeAttribute("which-jobs", goipp.TagKeyword, goipp.String(whichJobs)))
	}
	msg.Operation.Add(goipp.MakeAttr("requested-attributes", goipp.TagKeyword,
		goipp.String("job-id"),
		goipp.String("job-uri"),
		goipp.String("job-name"),
		goipp.String("job-state"),
		goipp.String("job-state-reasons")))

	payload, err := msg.EncodeBytes()
	if err != nil {
		return nil, &Error{Op: "Get-Jobs", Err: fmt.Errorf("编码 IPP 请求: %w", err)}
	}
	rsp, err := c.do(ctx, "Get-Jobs", printerURI, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}

	infos := make([]JobInfo, 0, len(rsp.Groups))
	for _, grp := range rsp.Groups {
		if grp.Tag != goipp.TagJobGroup {
			continue
		}
		infos = append(infos, jobInfoFromAttrs(grp.Attrs))
	}
	if len(infos) == 0 && len(rsp.Job) > 0 {
		infos = append(infos, jobInfoFromAttrs(rsp.Job))
	}
	return infos, nil
}

func jobInfoFromAttrs(attrs goipp.Attributes) JobInfo {
	info := JobInfo{
		URI:          attrString(attrs, "job-uri"),
		Name:         attrString(attrs, "job-name"),
		State:        jobStateKeyword(attrs),
		StateReasons: attrStrings(attrs, "job-state-reasons"),
		StateMessage: attrString(attrs, "job-state-message"),
	}
	if id, ok := attrInt(attrs, "job-id"); ok {
		info.ID = id
	}
	if state, ok := attrInt(attrs, "job-state"); ok {
		info.RawState = state
	}
	return info
}

func (c *Client) GetPrinter(ctx context.Context, printerURI string) (*PrinterInfo, error) {
	msg := goipp.NewRequest(goipp.DefaultVersion, goipp.OpGetPrinterAttributes, c.nextRequestID())
	msg.Operation.Add(goipp.MakeAttribute("attributes-charset", goipp.TagCharset, goipp.String("utf-8")))
	msg.Operation.Add(goipp.MakeAttribute("attributes-natural-language", goipp.TagLanguage, goipp.String("en-us")))
	msg.Operation.Add(goipp.MakeAttribute("printer-uri", goipp.TagURI, goipp.String(printerURI)))
	msg.Operation.Add(goipp.MakeAttr("requested-attributes", goipp.TagKeyword,
		goipp.String("printer-state"),
		goipp.String("printer-state-reasons"),
		goipp.String("printer-state-message"),
		goipp.String("printer-is-accepting-jobs"),
		goipp.String("printer-make-and-model")))

	payload, err := msg.EncodeBytes()
	if err != nil {
		return nil, &Error{Op: "Get-Printer-Attributes", Err: fmt.Errorf("编码 IPP 请求: %w", err)}
	}
	rsp, err := c.do(ctx, "Get-Printer-Attributes", printerURI, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}

	info := &PrinterInfo{
		State:        printerStateKeyword(rsp.Printer),
		StateReasons: attrStrings(rsp.Printer, "printer-state-reasons"),
		StateMessage: attrString(rsp.Printer, "printer-state-message"),
		MakeAndModel: attrString(rsp.Printer, "printer-make-and-model"),
	}
	if state, ok := attrInt(rsp.Printer, "printer-state"); ok {
		info.RawState = state
	}
	if accepting, ok := attrBool(rsp.Printer, "printer-is-accepting-jobs"); ok {
		info.AcceptingJobs = accepting
	}
	return info, nil
}

func (c *Client) do(ctx context.Context, op, printerURI string, body io.Reader) (*goipp.Message, error) {
	target, err := transportURL(printerURI)
	if err != nil {
		return nil, &Error{Op: op, Err: err}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, body)
	if err != nil {
		return nil, &Error{Op: op, Err: fmt.Errorf("构造 HTTP 请求: %w", err)}
	}
	req.Header.Set("Content-Type", goipp.ContentType)
	req.Header.Set("Accept", goipp.ContentType)

	if t := time.Duration(c.timeout.Load()); t > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t)
		defer cancel()
	}

	resp, err := c.http.Do(req)
	if err != nil {

		return nil, &Error{Op: op, Transient: true, Err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		transient := resp.StatusCode >= 500
		return nil, &Error{
			Op: op, HTTPCode: resp.StatusCode, Transient: transient,
			Err: fmt.Errorf("HTTP 状态异常"),
		}
	}

	var msg goipp.Message
	if err := msg.Decode(resp.Body); err != nil {
		return nil, &Error{Op: op, Transient: true, Err: fmt.Errorf("解析 IPP 响应: %w", err)}
	}

	status := goipp.Status(msg.Code)

	if int(status) >= 0x0100 {

		cause := errors.New("打印机拒绝该请求")
		if detail := attrString(msg.Operation, "status-message"); detail != "" {
			cause = fmt.Errorf("打印机拒绝该请求: %s", detail)
		}
		return nil, &Error{
			Op:     op,
			Status: status,

			Transient: int(status) >= statusServerErrorStart,
			Err:       cause,
		}
	}
	return &msg, nil
}

func (c *Client) nextRequestID() uint32 {
	return c.seq.Add(1)
}

func transportURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("解析 printer-uri %q: %w", raw, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "ipp":
		u.Scheme = "http"
	case "ipps":
		u.Scheme = "https"
	case "http", "https":
	default:
		return "", fmt.Errorf("不支持的 printer-uri scheme: %q", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("printer-uri 缺少主机名: %q", raw)
	}
	if u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), defaultPort)
	}
	if u.Path == "" {
		u.Path = "/ipp/print"
	}
	return u.String(), nil
}

func buildJobURI(printerURI string, jobID int64) string {
	if printerURI == "" || jobID <= 0 {
		return ""
	}
	return fmt.Sprintf("%s/jobs/%d", strings.TrimRight(printerURI, "/"), jobID)
}
