package ipp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/OpenPrinting/goipp"
)

func newFakePrinter(
	t *testing.T, respond func(op goipp.Op, req goipp.Message) *goipp.Message,
) (*httptest.Server, *[]goipp.Message) {
	t.Helper()

	var seen []goipp.Message
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("读取请求体失败: %v", err)
			return
		}
		var req goipp.Message
		if err := req.DecodeBytes(body); err != nil {
			t.Errorf("解析 IPP 请求失败: %v", err)
			return
		}
		seen = append(seen, req)

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
	return srv, &seen
}

func testClient() *Client {
	return NewClient(5*time.Second, "test")
}

func printerURIOf(srv *httptest.Server) string { return srv.URL + "/ipp/print" }

func TestGetJobFallsBackToJobIDOnNotFound(t *testing.T) {
	srv, seen := newFakePrinter(t, func(_ goipp.Op, req goipp.Message) *goipp.Message {
		if attrString(req.Operation, "job-uri") != "" {

			return goipp.NewResponse(goipp.DefaultVersion, goipp.Status(statusClientErrorNotFound), 0)
		}
		rsp := goipp.NewResponse(goipp.DefaultVersion, goipp.Status(0), 0)
		rsp.Job.Add(goipp.MakeAttribute("job-id", goipp.TagInteger, goipp.Integer(77)))
		rsp.Job.Add(goipp.MakeAttribute("job-state", goipp.TagEnum, goipp.Integer(jobStateCompleted)))
		return rsp
	})

	info, err := testClient().GetJob(context.Background(), printerURIOf(srv), "ipp://wrong/jobs/77", 77)
	if err != nil {
		t.Fatalf("GetJob 应当退回 job-id 寻址并成功，实际报错: %v", err)
	}
	if info.State != "completed" {
		t.Errorf("作业状态 = %q，期望 completed", info.State)
	}
	if len(*seen) != 2 {
		t.Fatalf("应当发出 2 次请求（job-uri 一次、job-id 一次），实际 %d 次", len(*seen))
	}
	if got := attrInt64(t, (*seen)[1].Operation, "job-id"); got != 77 {
		t.Errorf("退回请求应当带上 job-id=77，实际 %d", got)
	}
	if attrString((*seen)[1].Operation, "job-uri") != "" {
		t.Error("退回请求不应再带 job-uri")
	}
}

func TestGetJobPrefersJobURI(t *testing.T) {
	srv, seen := newFakePrinter(t, func(_ goipp.Op, _ goipp.Message) *goipp.Message {
		rsp := goipp.NewResponse(goipp.DefaultVersion, goipp.Status(0), 0)
		rsp.Job.Add(goipp.MakeAttribute("job-id", goipp.TagInteger, goipp.Integer(9)))
		rsp.Job.Add(goipp.MakeAttribute("job-state", goipp.TagEnum, goipp.Integer(jobStateProcessing)))
		return rsp
	})

	info, err := testClient().GetJob(context.Background(), printerURIOf(srv), "ipp://printer/jobs/9", 9)
	if err != nil {
		t.Fatalf("GetJob 报错: %v", err)
	}
	if info.State != "processing" {
		t.Errorf("作业状态 = %q，期望 processing", info.State)
	}
	if len(*seen) != 1 {
		t.Fatalf("job-uri 可用时只应发一次请求，实际 %d 次", len(*seen))
	}
	if got := attrString((*seen)[0].Operation, "job-uri"); got != "ipp://printer/jobs/9" {
		t.Errorf("首选请求应当带 job-uri，实际 %q", got)
	}

	if got := attrString((*seen)[0].Operation, "printer-uri"); got != "" {
		t.Errorf("job-uri 寻址不应同时带 printer-uri（CUPS 会直接 bad-request），实际 %q", got)
	}
}

func TestGetJobFallsBackWhenJobURIIsRejected(t *testing.T) {
	srv, seen := newFakePrinter(t, func(_ goipp.Op, req goipp.Message) *goipp.Message {
		if attrString(req.Operation, "job-uri") != "" {
			return goipp.NewResponse(goipp.DefaultVersion,
				goipp.Status(statusClientErrorBadRequest), 0)
		}
		rsp := goipp.NewResponse(goipp.DefaultVersion, goipp.Status(0), 0)
		rsp.Job.Add(goipp.MakeAttribute("job-id", goipp.TagInteger, goipp.Integer(88)))
		rsp.Job.Add(goipp.MakeAttribute("job-state", goipp.TagEnum, goipp.Integer(jobStateCompleted)))
		return rsp
	})

	info, err := testClient().GetJob(context.Background(), printerURIOf(srv), "ipp://printer/jobs/88", 88)
	if err != nil {
		t.Fatalf("job-uri 被拒后应当退回 job-id 寻址，实际报错: %v", err)
	}
	if info.State != "completed" {
		t.Errorf("作业状态 = %q，期望 completed", info.State)
	}
	if len(*seen) != 2 {
		t.Fatalf("应当发出 2 次请求（job-uri 一次、job-id 一次），实际 %d 次", len(*seen))
	}
	if got := attrInt64(t, (*seen)[1].Operation, "job-id"); got != 88 {
		t.Errorf("退回请求应当带上 job-id=88，实际 %d", got)
	}
	if attrString((*seen)[1].Operation, "printer-uri") == "" {
		t.Error("退回请求应当带上 printer-uri（这才是 printer-uri + job-id 的合法组合）")
	}
}

func TestGetJobSurfacesStatusMessage(t *testing.T) {
	const detail = "Got a printer-uri attribute but no job-id."
	srv, _ := newFakePrinter(t, func(_ goipp.Op, _ goipp.Message) *goipp.Message {
		rsp := goipp.NewResponse(goipp.DefaultVersion, goipp.Status(statusClientErrorBadRequest), 0)
		rsp.Operation.Add(goipp.MakeAttribute("status-message", goipp.TagText, goipp.String(detail)))
		return rsp
	})

	_, err := testClient().GetJob(context.Background(), printerURIOf(srv), "", 1)
	if err == nil {
		t.Fatal("打印机拒绝请求时 GetJob 应当报错")
	}
	if !strings.Contains(err.Error(), detail) {
		t.Errorf("错误信息应当带上打印机的 status-message，实际: %v", err)
	}
}

func TestStatusClassificationDrivesRetry(t *testing.T) {
	cases := []struct {
		name          string
		status        int
		wantTransient bool
		wantNotFound  bool
	}{
		{"client-error-not-found（作业已被清理）", statusClientErrorNotFound, false, true},
		{"client-error-gone（作业已删除）", statusClientErrorGone, false, true},
		{"client-error-document-format-not-supported（重试无意义）", 0x040A, false, false},
		{"client-error-bad-request", 0x0400, false, false},
		{"server-error-internal-error（值得重试）", 0x0500, true, false},
		{"server-error-busy（值得重试）", 0x0507, true, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := newFakePrinter(t, func(_ goipp.Op, _ goipp.Message) *goipp.Message {
				return goipp.NewResponse(goipp.DefaultVersion, goipp.Status(c.status), 0)
			})

			_, err := testClient().GetJob(context.Background(), printerURIOf(srv), "", 1)
			if err == nil {
				t.Fatal("打印机返回错误状态码时 GetJob 应当报错")
			}
			if got := IsTransient(err); got != c.wantTransient {
				t.Errorf("IsTransient = %v，期望 %v（status=0x%04X）", got, c.wantTransient, c.status)
			}
			if got := IsJobNotFound(err); got != c.wantNotFound {
				t.Errorf("IsJobNotFound = %v，期望 %v（status=0x%04X）", got, c.wantNotFound, c.status)
			}
		})
	}
}

func TestGetJobsParsesRepeatedJobGroups(t *testing.T) {
	jobAttrs := func(id, state int) goipp.Attributes {
		var attrs goipp.Attributes
		attrs.Add(goipp.MakeAttribute("job-id", goipp.TagInteger, goipp.Integer(id)))
		attrs.Add(goipp.MakeAttribute("job-state", goipp.TagEnum, goipp.Integer(state)))
		return attrs
	}

	srv, seen := newFakePrinter(t, func(_ goipp.Op, _ goipp.Message) *goipp.Message {
		rsp := goipp.NewResponse(goipp.DefaultVersion, goipp.Status(0), 0)
		rsp.Groups = goipp.Groups{
			{Tag: goipp.TagOperationGroup, Attrs: goipp.Attributes{}},
			{Tag: goipp.TagJobGroup, Attrs: jobAttrs(12, jobStateProcessing)},
			{Tag: goipp.TagJobGroup, Attrs: jobAttrs(13, jobStatePending)},
		}
		return rsp
	})

	jobs, err := testClient().GetJobs(context.Background(), printerURIOf(srv), "not-completed")
	if err != nil {
		t.Fatalf("GetJobs 报错: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("应当解析出 2 个作业，实际 %d 个", len(jobs))
	}
	if jobs[0].ID != 12 || jobs[0].State != "processing" {
		t.Errorf("第一个作业 = {id:%d state:%q}，期望 {12 processing}", jobs[0].ID, jobs[0].State)
	}
	if jobs[1].ID != 13 || jobs[1].State != "pending" {
		t.Errorf("第二个作业 = {id:%d state:%q}，期望 {13 pending}", jobs[1].ID, jobs[1].State)
	}
	if got := attrString((*seen)[0].Operation, "which-jobs"); got != "not-completed" {
		t.Errorf("which-jobs = %q，期望 not-completed", got)
	}
}

func TestGetJobsEmptyResponse(t *testing.T) {
	srv, _ := newFakePrinter(t, func(_ goipp.Op, _ goipp.Message) *goipp.Message {
		return goipp.NewResponse(goipp.DefaultVersion, goipp.Status(0), 0)
	})

	jobs, err := testClient().GetJobs(context.Background(), printerURIOf(srv), "not-completed")
	if err != nil {
		t.Fatalf("GetJobs 报错: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("应当返回空列表，实际 %d 个", len(jobs))
	}
}

func attrInt64(t *testing.T, attrs goipp.Attributes, name string) int64 {
	t.Helper()
	v, ok := attrInt(attrs, name)
	if !ok {
		t.Fatalf("缺少整数属性 %s", name)
	}
	return v
}
