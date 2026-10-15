package service

import (
	"strings"
	"testing"
	"time"

	"github.com/Sparkzure/PayPrint/server/internal/domain"
	"github.com/Sparkzure/PayPrint/server/internal/store"
)

func (e *paymentTestEnv) createJob(
	id, orderID string, status domain.JobStatus, attempts, maxAttempts, lockedAt int64,
) store.PrintJob {
	e.t.Helper()
	e.mustExec(`INSERT INTO print_jobs (id, order_id, printer_id, status, attempts,
		max_attempts, cups_job_id, ipp_job_uri, job_state, last_error, next_retry_at,
		locked_at, locked_by, submitted_at, finished_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, '', '', '', 0, ?, '', 0, 0, ?, ?)`,
		id, orderID, e.siteA, string(status), attempts, maxAttempts, lockedAt,
		e.now.Unix(), e.now.Unix())

	job, err := e.svc.Q.GetJobByID(e.ctx(), id)
	if err != nil {
		e.t.Fatalf("读取打印任务 %s: %v", id, err)
	}
	return job
}

func (e *paymentTestEnv) jobStatus(orderID string) string {
	e.t.Helper()
	job, err := e.svc.Q.GetJobByOrderID(e.ctx(), orderID)
	if err != nil {
		e.t.Fatalf("读取订单 %s 的打印任务: %v", orderID, err)
	}
	return job.Status
}

func (e *paymentTestEnv) addPaymentLedger(order store.Order) {
	e.t.Helper()
	e.mustExec(`INSERT INTO ledger_entries (id, order_id, user_id, entry_type,
		amount_fen, spent_after_fen, channel, memo, created_at)
		VALUES (?, ?, ?, 'payment', ?, ?, 'wechat', '', ?)`,
		"ledger-"+order.ID, order.ID, order.UserID, order.AmountFen, order.AmountFen,
		e.now.Unix())
}

func TestRefundCancelsPendingJob(t *testing.T) {
	e := newPaymentTestEnv(t)
	order, _ := e.seedUserOrder("refund", e.siteA, 60, domain.OrderQueued,
		e.now.Add(-time.Minute).Unix(), e.now.Add(10*time.Minute).Unix())
	e.addPaymentLedger(order)
	e.createJob("job-refund", order.ID, domain.JobPending, 0, 5, 0)

	if err := e.svc.Payments.Refund(e.ctx(), RefundInput{
		OrderID: order.ID, ActorID: "admin", Reason: "用户不打了",
	}); err != nil {
		t.Fatalf("退款失败: %v", err)
	}

	if got := e.jobStatus(order.ID); got != string(domain.JobCancelled) {
		t.Fatalf("尚未投递的任务应被取消，实际 %s", got)
	}
	if got := e.order(order.ID).Status; got != string(domain.OrderRefunded) {
		t.Fatalf("订单应为已退款，实际 %s", got)
	}
}

func TestRefundDoesNotPretendToCancelInFlightJob(t *testing.T) {
	e := newPaymentTestEnv(t)
	order, _ := e.seedUserOrder("inflight", e.siteA, 60, domain.OrderPrinting,
		e.now.Add(-time.Minute).Unix(), e.now.Add(10*time.Minute).Unix())
	e.addPaymentLedger(order)
	e.createJob("job-inflight", order.ID, domain.JobSubmitted, 1, 5, 0)

	if err := e.svc.Payments.Refund(e.ctx(), RefundInput{
		OrderID: order.ID, ActorID: "admin",
	}); err != nil {
		t.Fatalf("退款失败: %v", err)
	}

	if got := e.jobStatus(order.ID); got != string(domain.JobSubmitted) {
		t.Fatalf("已投出的任务不应被改写，实际 %s", got)
	}
	if got := e.order(order.ID).Status; got != string(domain.OrderRefunded) {
		t.Fatalf("钱不能压着，订单仍应退款，实际 %s", got)
	}
}

func TestRetryOrderRefusesInFlightJob(t *testing.T) {
	for _, status := range []domain.JobStatus{
		domain.JobSubmitting, domain.JobSubmitted, domain.JobPrinting, domain.JobCompleted,
	} {
		e := newPaymentTestEnv(t)
		order, _ := e.seedUserOrder("retry", e.siteA, 60, domain.OrderPrinting,
			e.now.Add(-time.Minute).Unix(), e.now.Add(10*time.Minute).Unix())
		e.createJob("job-flight", order.ID, status, 1, 5, 0)

		err := e.svc.Jobs.RetryOrder(e.ctx(), order.ID, "admin")
		requireErrorCode(t, err, "conflict")

		if got := e.jobStatus(order.ID); got != string(status) {
			t.Fatalf("状态 %s 的任务不应被重推，实际变成 %s", status, got)
		}
	}
}

func TestRetryOrderRefusesCancelledJob(t *testing.T) {
	e := newPaymentTestEnv(t)
	order, _ := e.seedUserOrder("retry-cancel", e.siteA, 60, domain.OrderRefunded,
		e.now.Add(-time.Minute).Unix(), e.now.Add(10*time.Minute).Unix())
	e.createJob("job-cancelled", order.ID, domain.JobCancelled, 0, 5, 0)

	err := e.svc.Jobs.RetryOrder(e.ctx(), order.ID, "admin")
	requireErrorCode(t, err, "conflict")
	if got := e.jobStatus(order.ID); got != string(domain.JobCancelled) {
		t.Fatalf("已取消的任务不应被重推，实际 %s", got)
	}
}

func TestRetryOrderRequeuesStuckJob(t *testing.T) {
	e := newPaymentTestEnv(t)
	order, _ := e.seedUserOrder("stuck", e.siteA, 60, domain.OrderQueued,
		e.now.Add(-time.Minute).Unix(), e.now.Add(10*time.Minute).Unix())
	e.createJob("job-stuck", order.ID, domain.JobNeedsReview, 5, 5, 0)

	if err := e.svc.Jobs.RetryOrder(e.ctx(), order.ID, "admin"); err != nil {
		t.Fatalf("needs_review 的任务应当允许重推: %v", err)
	}

	job, err := e.svc.Q.GetJobByOrderID(e.ctx(), order.ID)
	if err != nil {
		t.Fatalf("读取任务: %v", err)
	}
	if job.Status != string(domain.JobPending) {
		t.Fatalf("重推后应回到 pending，实际 %s", job.Status)
	}
	if job.Attempts != 0 {
		t.Fatalf("人工重推应当重置重试次数，实际 %d", job.Attempts)
	}
	if job.FinishedAt != 0 {
		t.Fatalf("重推后不应保留旧的完成时间，实际 %d", job.FinishedAt)
	}
}

func TestRecoverOrphansRespectsLease(t *testing.T) {
	e := newPaymentTestEnv(t)

	stale, _ := e.seedUserOrder("orphan-stale", e.siteA, 60, domain.OrderQueued,
		e.now.Add(-time.Minute).Unix(), e.now.Add(10*time.Minute).Unix())
	fresh, _ := e.seedUserOrder("orphan-fresh", e.siteA, 60, domain.OrderQueued,
		e.now.Add(-time.Minute).Unix(), e.now.Add(10*time.Minute).Unix())

	e.createJob("job-stale", stale.ID, domain.JobSubmitting, 1, 5,
		e.now.Add(-10*time.Minute).Unix())
	e.createJob("job-fresh", fresh.ID, domain.JobSubmitting, 1, 5,
		e.now.Add(-10*time.Second).Unix())

	recovered, err := e.svc.Jobs.RecoverOrphans(e.ctx(), 10)
	if err != nil {
		t.Fatalf("回收孤儿任务失败: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("应当只回收超过租约的那一个，实际 %d", recovered)
	}
	if got := e.jobStatus(stale.ID); got != string(domain.JobPending) {
		t.Fatalf("被遗弃的任务应重新排队，实际 %s", got)
	}
	if got := e.jobStatus(fresh.ID); got != string(domain.JobSubmitting) {
		t.Fatalf("租约未到期的任务不应被抢走，实际 %s", got)
	}
}

func TestRecoverOrphansEscalatesExhaustedJob(t *testing.T) {
	e := newPaymentTestEnv(t)
	order, _ := e.seedUserOrder("orphan-max", e.siteA, 60, domain.OrderQueued,
		e.now.Add(-time.Minute).Unix(), e.now.Add(10*time.Minute).Unix())
	e.createJob("job-max", order.ID, domain.JobSubmitting, 5, 5,
		e.now.Add(-10*time.Minute).Unix())

	if _, err := e.svc.Jobs.RecoverOrphans(e.ctx(), 10); err != nil {
		t.Fatalf("回收孤儿任务失败: %v", err)
	}

	if got := e.jobStatus(order.ID); got != string(domain.JobNeedsReview) {
		t.Fatalf("重试耗尽的孤儿应转人工，实际 %s", got)
	}
	if got := e.order(order.ID).Status; got != string(domain.OrderRefundPending) {
		t.Fatalf("已收钱的订单应转待退款，实际 %s", got)
	}
}

func TestInterruptedJobStaysPollable(t *testing.T) {
	e := newPaymentTestEnv(t)
	order, _ := e.seedUserOrder("interrupted", e.siteA, 60, domain.OrderPrinting,
		e.now.Add(-time.Minute).Unix(), e.now.Add(10*time.Minute).Unix())
	job := e.createJob("job-interrupted", order.ID, domain.JobInterrupted, 1, 5, 0)

	inFlight, err := e.svc.Q.ListJobsInFlight(e.ctx(), 10)
	if err != nil {
		t.Fatalf("查询在途作业失败: %v", err)
	}
	found := false
	for _, row := range inFlight {
		if row.ID == job.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("中断中的作业应当仍被 ListJobsInFlight 选中，否则永远等不到故障恢复")
	}

	affected, err := e.svc.Q.UpdateJobState(e.ctx(), store.UpdateJobStateParams{
		Status:     string(domain.JobCompleted),
		JobState:   "completed",
		FinishedAt: e.now.Unix(),
		UpdatedAt:  e.now.Unix(),
		ID:         job.ID,
	})
	if err != nil {
		t.Fatalf("更新作业状态失败: %v", err)
	}
	if affected != 1 {
		t.Fatalf("中断中的作业必须能被回查更新，否则会永久滞留（影响行数 = %d）", affected)
	}
}

func TestDecodeOrderPrintJobOnlyTrustsDetectedPageCount(t *testing.T) {
	paramsJSON := `{"copies":1,"duplex":"one_sided","color_mode":"monochrome",` +
		`"paper_size":"A4","page_range":"","n_up":1,"orientation":"portrait",` +
		`"quality":"normal","fit_to_page":true}`
	costJSON := `{"unit_price_fen":60,"sides_per_copy":1,"sheets_per_copy":1,` +
		`"copies":1,"pages_total":1,"total_fen":60,"currency":"CNY",` +
		`"color_mode":"monochrome","n_up":1,"duplex":"one_sided",` +
		`"page_ranges":[{"from":1,"to":1}],"explanation":""}`
	order := store.Order{ParamsJson: paramsJSON, CostJson: costJSON}

	declared := store.PrintFile{
		PageCount:       1,
		PageCountSource: string(domain.PageCountDeclared),
	}
	if _, ranges, err := decodeOrderPrintJob(order, declared); err != nil {
		t.Fatalf("解析打印参数失败: %v", err)
	} else if len(ranges) == 0 {
		t.Fatal("页数来自客户端上报时必须下发 page-ranges，否则打印范围与计费范围会脱钩")
	}

	detected := store.PrintFile{
		PageCount:       1,
		PageCountSource: string(domain.PageCountDetected),
	}
	if _, ranges, err := decodeOrderPrintJob(order, detected); err != nil {
		t.Fatalf("解析打印参数失败: %v", err)
	} else if len(ranges) != 0 {
		t.Fatal("页数由服务端数出时才允许走整篇打印优化")
	}
}

func TestUploadRejectsOversizedPageCountHint(t *testing.T) {
	e := newPaymentTestEnv(t)
	userID := e.createUser("uploader", domain.RoleUser)

	content := "%PDF-1.4\n%%EOF\n"

	requireErrorCode(t,
		mustUpload(t, e, userID, content, domain.MaxPageCount+1), "too_many_pages")

	view, err := e.svc.Files.Upload(e.ctx(), UploadInput{
		UserID:        userID,
		Filename:      "ok.pdf",
		ContentType:   "application/pdf",
		DeclaredSize:  int64(len(content)),
		Reader:        strings.NewReader(content),
		PageCountHint: 12,
	})
	if err != nil {
		t.Fatalf("上限内的页数应当接受: %v", err)
	}
	if view.PageCount != 12 {
		t.Fatalf("应当采用客户端上报的页数，实际 %d", view.PageCount)
	}

	file, err := e.svc.Q.GetPrintFileForUser(e.ctx(), store.GetPrintFileForUserParams{
		ID: view.ID, UserID: userID,
	})
	if err != nil {
		t.Fatalf("读取文件: %v", err)
	}
	if file.PageCountSource != string(domain.PageCountDeclared) {
		t.Fatalf("客户端上报的页数必须标记为 declared，实际 %s", file.PageCountSource)
	}
}

func mustUpload(t *testing.T, e *paymentTestEnv, userID, content string, hint int) error {
	t.Helper()
	_, err := e.svc.Files.Upload(e.ctx(), UploadInput{
		UserID:        userID,
		Filename:      "huge.pdf",
		ContentType:   "application/pdf",
		DeclaredSize:  int64(len(content)),
		Reader:        strings.NewReader(content),
		PageCountHint: hint,
	})
	return err
}

func TestListPaymentEventsSurfacesUnresolved(t *testing.T) {
	e := newPaymentTestEnv(t)
	collector := e.collectorEnv()

	if _, err := e.svc.Payments.Report(e.ctx(), ReportInput{
		Report:             collectorReport("evt-orphan", 999999, e.now.Unix()),
		CollectorPrinterID: collector,
	}); err != nil {
		requireErrorCode(t, err, "payment_unmatched")
	}

	views, err := e.svc.Payments.ListPaymentEvents(e.ctx(), "unresolved", 10, 0)
	if err != nil {
		t.Fatalf("查询支付事件失败: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("应当有 1 条待人工处理事件，实际 %d", len(views))
	}
	if views[0].AmountFen != 999999 {
		t.Fatalf("金额应原样保留，实际 %d", views[0].AmountFen)
	}
	if views[0].OrderID != "" {
		t.Fatalf("没匹配到订单的事件不应有 order_id，实际 %q", views[0].OrderID)
	}
	if views[0].StatusText == "" || views[0].Tone == "" {
		t.Fatalf("事件视图应带上文案与语义色：%+v", views[0])
	}

	if _, err := e.svc.Payments.ListPaymentEvents(e.ctx(), "nope", 10, 0); err == nil {
		t.Fatal("非法的 status 应当被拒绝")
	}
	accepted, err := e.svc.Payments.ListPaymentEvents(e.ctx(), "accepted", 10, 0)
	if err != nil {
		t.Fatalf("按状态查询失败: %v", err)
	}
	if len(accepted) != 0 {
		t.Fatalf("不该有已被放行的事件，实际 %d", len(accepted))
	}
}
