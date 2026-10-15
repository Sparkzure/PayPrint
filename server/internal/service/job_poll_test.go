package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/OpenPrinting/goipp"

	"github.com/Sparkzure/PayPrint/server/internal/domain"
	"github.com/Sparkzure/PayPrint/server/internal/ipp"
	"github.com/Sparkzure/PayPrint/server/internal/store"
)

func fakeIPPPrinter(
	t *testing.T, respond func(op goipp.Op, req goipp.Message) *goipp.Message,
) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("读取 IPP 请求失败: %v", err)
			return
		}
		var req goipp.Message
		if err := req.DecodeBytes(body); err != nil {
			t.Errorf("解析 IPP 请求失败: %v", err)
			return
		}
		rsp := respond(goipp.Op(req.Code), req)
		if rsp == nil {
			t.Errorf("测试用例没有为操作 %v 准备响应", goipp.Op(req.Code))
			return
		}
		rsp.RequestID = req.RequestID
		w.Header().Set("Content-Type", goipp.ContentType)
		if err := rsp.Encode(w); err != nil {
			t.Errorf("编码 IPP 响应失败: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func jobResponse(jobID, state int) *goipp.Message {
	rsp := goipp.NewResponse(goipp.DefaultVersion, goipp.Status(0), 0)
	rsp.Job.Add(goipp.MakeAttribute("job-id", goipp.TagInteger, goipp.Integer(jobID)))
	rsp.Job.Add(goipp.MakeAttribute("job-state", goipp.TagEnum, goipp.Integer(state)))
	return rsp
}

func (e *paymentTestEnv) attachPrinter(srv *httptest.Server) {
	e.t.Helper()
	e.svc.IPP = ipp.NewClient(2*time.Second, "test")
	e.mustExec(`UPDATE printers SET cups_uri = ? WHERE id = ?`,
		srv.URL+"/ipp/print", e.siteA)
}

func (e *paymentTestEnv) createSubmittedJob(
	id, orderID string, cupsJobID, submittedAt int64,
) store.PrintJob {
	e.t.Helper()
	e.mustExec(`INSERT INTO print_jobs (id, order_id, printer_id, status, attempts,
		max_attempts, cups_job_id, ipp_job_uri, job_state, last_error, next_retry_at,
		locked_at, locked_by, submitted_at, finished_at, created_at, updated_at)
		VALUES (?, ?, ?, 'submitted', 1, 5, ?, '', 'processing', '', 0, 0, '', ?, 0, ?, ?)`,
		id, orderID, e.siteA, cupsJobID, submittedAt, e.now.Unix(), e.now.Unix())

	job, err := e.svc.Q.GetJobByID(e.ctx(), id)
	if err != nil {
		e.t.Fatalf("读取打印任务 %s: %v", id, err)
	}
	return job
}

func TestPollInFlightCompletesOrderWhenJobPurged(t *testing.T) {
	e := newPaymentTestEnv(t)
	srv := fakeIPPPrinter(t, func(op goipp.Op, _ goipp.Message) *goipp.Message {
		switch op {
		case goipp.OpGetJobAttributes:

			return goipp.NewResponse(goipp.DefaultVersion,
				goipp.Status(0x0406), 0)
		case goipp.OpGetJobs:

			return goipp.NewResponse(goipp.DefaultVersion, goipp.Status(0), 0)
		default:
			return nil
		}
	})
	e.attachPrinter(srv)

	order, _ := e.seedUserOrder("purged", e.siteA, 60, domain.OrderPrinting,
		e.now.Add(-10*time.Minute).Unix(), e.now.Add(10*time.Minute).Unix())
	e.createSubmittedJob("job-purged", order.ID, 42, e.now.Add(-5*time.Minute).Unix())

	if _, err := e.svc.Jobs.PollInFlight(e.ctx(), 10); err != nil {
		t.Fatalf("回查失败: %v", err)
	}

	if got := e.jobStatus(order.ID); got != string(domain.JobCompleted) {
		t.Fatalf("作业应被判为已完成，实际 %s", got)
	}
	if got := e.order(order.ID).Status; got != string(domain.OrderCompleted) {
		t.Fatalf("订单必须跟着结束，否则界面永远停在打印中，实际 %s", got)
	}
}

func TestPollInFlightWaitsOutGracePeriod(t *testing.T) {
	e := newPaymentTestEnv(t)
	srv := fakeIPPPrinter(t, func(op goipp.Op, _ goipp.Message) *goipp.Message {
		switch op {
		case goipp.OpGetJobAttributes:
			return goipp.NewResponse(goipp.DefaultVersion, goipp.Status(0x0406), 0)
		case goipp.OpGetJobs:
			return goipp.NewResponse(goipp.DefaultVersion, goipp.Status(0), 0)
		default:
			return nil
		}
	})
	e.attachPrinter(srv)

	order, _ := e.seedUserOrder("grace", e.siteA, 60, domain.OrderPrinting,
		e.now.Add(-time.Minute).Unix(), e.now.Add(10*time.Minute).Unix())

	e.createSubmittedJob("job-grace", order.ID, 43, e.now.Unix())

	if _, err := e.svc.Jobs.PollInFlight(e.ctx(), 10); err != nil {
		t.Fatalf("回查失败: %v", err)
	}

	if got := e.jobStatus(order.ID); got != string(domain.JobSubmitted) {
		t.Fatalf("宽限期内不应下结论，实际 %s", got)
	}
	if got := e.order(order.ID).Status; got != string(domain.OrderPrinting) {
		t.Fatalf("订单不应被推进，实际 %s", got)
	}
}

func TestPollInFlightKeepsTrackingJobStillInQueue(t *testing.T) {
	e := newPaymentTestEnv(t)
	srv := fakeIPPPrinter(t, func(op goipp.Op, req goipp.Message) *goipp.Message {
		switch op {
		case goipp.OpGetJobAttributes:

			if attrStr(req.Operation, "printer-uri") == "" {
				t.Error("Get-Job-Attributes 请求必须带 printer-uri")
			}
			return goipp.NewResponse(goipp.DefaultVersion, goipp.Status(0x0406), 0)
		case goipp.OpGetJobs:

			rsp := goipp.NewResponse(goipp.DefaultVersion, goipp.Status(0), 0)
			rsp.Groups = goipp.Groups{
				{Tag: goipp.TagOperationGroup, Attrs: goipp.Attributes{}},
				{Tag: goipp.TagJobGroup, Attrs: goipp.Attributes{
					goipp.MakeAttribute("job-id", goipp.TagInteger, goipp.Integer(44)),
					goipp.MakeAttribute("job-state", goipp.TagEnum, goipp.Integer(5)),
				}},
			}
			return rsp
		default:
			return nil
		}
	})
	e.attachPrinter(srv)

	order, _ := e.seedUserOrder("queued", e.siteA, 60, domain.OrderPrinting,
		e.now.Add(-10*time.Minute).Unix(), e.now.Add(10*time.Minute).Unix())
	e.createSubmittedJob("job-queued", order.ID, 44, e.now.Add(-5*time.Minute).Unix())

	if _, err := e.svc.Jobs.PollInFlight(e.ctx(), 10); err != nil {
		t.Fatalf("回查失败: %v", err)
	}

	if got := e.jobStatus(order.ID); got != string(domain.JobPrinting) {
		t.Fatalf("仍在队列里的作业应继续跟踪为打印中，实际 %s", got)
	}
	if got := e.order(order.ID).Status; got != string(domain.OrderPrinting) {
		t.Fatalf("订单不应被提前判完成，实际 %s", got)
	}
}

func TestPollInFlightCompletesOrderOnReportedState(t *testing.T) {
	e := newPaymentTestEnv(t)
	srv := fakeIPPPrinter(t, func(op goipp.Op, _ goipp.Message) *goipp.Message {
		if op != goipp.OpGetJobAttributes {
			return nil
		}
		return jobResponse(45, 9)
	})
	e.attachPrinter(srv)

	order, _ := e.seedUserOrder("reported", e.siteA, 60, domain.OrderPrinting,
		e.now.Add(-10*time.Minute).Unix(), e.now.Add(10*time.Minute).Unix())
	e.createSubmittedJob("job-reported", order.ID, 45, e.now.Add(-5*time.Minute).Unix())

	if _, err := e.svc.Jobs.PollInFlight(e.ctx(), 10); err != nil {
		t.Fatalf("回查失败: %v", err)
	}

	if got := e.jobStatus(order.ID); got != string(domain.JobCompleted) {
		t.Fatalf("作业应为已完成，实际 %s", got)
	}
	got := e.order(order.ID)
	if got.Status != string(domain.OrderCompleted) {
		t.Fatalf("订单应为已完成，实际 %s", got.Status)
	}
	if got.CompletedAt == 0 {
		t.Fatal("订单应当写上完成时间")
	}
}

func TestPollInFlightCompletesOrderAgainstStrictCUPS(t *testing.T) {
	e := newPaymentTestEnv(t)

	hasAttr := func(attrs goipp.Attributes, name string) bool {
		for _, a := range attrs {
			if a.Name == name {
				return true
			}
		}
		return false
	}

	srv := fakeIPPPrinter(t, func(op goipp.Op, req goipp.Message) *goipp.Message {
		if op != goipp.OpGetJobAttributes {
			return nil
		}
		if attrStr(req.Operation, "printer-uri") != "" && !hasAttr(req.Operation, "job-id") {
			rsp := goipp.NewResponse(goipp.DefaultVersion, goipp.Status(0x0400), 0)
			rsp.Operation.Add(goipp.MakeAttribute("status-message", goipp.TagText,
				goipp.String("Got a printer-uri attribute but no job-id.")))
			return rsp
		}
		return jobResponse(77, 9)
	})
	e.attachPrinter(srv)

	order, _ := e.seedUserOrder("strict-cups", e.siteA, 60, domain.OrderPrinting,
		e.now.Add(-10*time.Minute).Unix(), e.now.Add(10*time.Minute).Unix())
	job := e.createSubmittedJob("job-strict-cups", order.ID, 77, e.now.Add(-5*time.Minute).Unix())

	e.mustExec(`UPDATE print_jobs SET ipp_job_uri = ? WHERE id = ?`,
		"ipp://cups/jobs/77", job.ID)

	if _, err := e.svc.Jobs.PollInFlight(e.ctx(), 10); err != nil {
		t.Fatalf("回查失败: %v", err)
	}

	if got := e.jobStatus(order.ID); got != string(domain.JobCompleted) {
		t.Fatalf("作业应为已完成，实际 %s", got)
	}
	if got := e.order(order.ID).Status; got != string(domain.OrderCompleted) {
		t.Fatalf("订单必须跟着结束，否则界面永远停在打印中，实际 %s", got)
	}
}

func TestReconcileStrandedOrdersAdvancesOrder(t *testing.T) {
	e := newPaymentTestEnv(t)

	completed, _ := e.seedUserOrder("stranded-done", e.siteA, 60, domain.OrderPrinting,
		e.now.Add(-10*time.Minute).Unix(), e.now.Add(10*time.Minute).Unix())
	e.createJob("job-stranded-done", completed.ID, domain.JobCompleted, 1, 5, 0)

	failed, _ := e.seedUserOrder("stranded-failed", e.siteA, 60, domain.OrderPrinting,
		e.now.Add(-10*time.Minute).Unix(), e.now.Add(10*time.Minute).Unix())
	e.createJob("job-stranded-failed", failed.ID, domain.JobFailed, 5, 5, 0)

	fixed, err := e.svc.Jobs.ReconcileStrandedOrders(e.ctx(), 10)
	if err != nil {
		t.Fatalf("补推进失败: %v", err)
	}
	if fixed != 2 {
		t.Fatalf("应当补推进 2 笔，实际 %d", fixed)
	}

	if got := e.order(completed.ID); got.Status != string(domain.OrderCompleted) {
		t.Fatalf("任务已完成的订单应补推进为已完成，实际 %s", got.Status)
	}

	if got := e.order(failed.ID); got.Status != string(domain.OrderRefundPending) {
		t.Fatalf("任务失败的订单应转待退款，实际 %s", got.Status)
	}

	if got := e.jobStatus(completed.ID); got != string(domain.JobCompleted) {
		t.Fatalf("补推进不应改动任务状态，实际 %s", got)
	}
	if got := e.jobStatus(failed.ID); got != string(domain.JobFailed) {
		t.Fatalf("补推进不应把失败任务重新排队，实际 %s", got)
	}

	again, err := e.svc.Jobs.ReconcileStrandedOrders(e.ctx(), 10)
	if err != nil {
		t.Fatalf("重复补推进失败: %v", err)
	}
	if again != 0 {
		t.Fatalf("补推进应当幂等，第二次不应再动任何订单，实际 %d", again)
	}
}

func attrStr(attrs goipp.Attributes, name string) string {
	for _, attr := range attrs {
		if attr.Name != name || len(attr.Values) == 0 {
			continue
		}
		if v, ok := attr.Values[0].V.(goipp.String); ok {
			return string(v)
		}
	}
	return ""
}
