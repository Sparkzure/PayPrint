package service

import (
	"testing"
	"time"

	"github.com/Sparkzure/PayPrint/server/internal/domain"
)

func (e *paymentTestEnv) setInflight(jobID string, at time.Time) {
	e.t.Helper()
	e.mustExec(`UPDATE print_jobs SET inflight_since = ? WHERE id = ?`, at.Unix(), jobID)
}

func TestEscalateStuckJobs(t *testing.T) {
	tests := []struct {
		name string

		status    domain.JobStatus
		amountFen int64
		age       time.Duration

		timed     bool
		wantCount int
		wantJob   domain.JobStatus
		wantOrder domain.OrderStatus
	}{
		{
			name: "已入队超过阈值转人工", status: domain.JobSubmitted, amountFen: 60,
			age: 2 * time.Hour, timed: true,
			wantCount: 1, wantJob: domain.JobNeedsReview, wantOrder: domain.OrderRefundPending,
		},
		{
			name: "打印中超过阈值转人工", status: domain.JobPrinting, amountFen: 60,
			age: 2 * time.Hour, timed: true,
			wantCount: 1, wantJob: domain.JobNeedsReview, wantOrder: domain.OrderRefundPending,
		},
		{
			name: "打印中断超过阈值转人工", status: domain.JobInterrupted, amountFen: 60,
			age: 2 * time.Hour, timed: true,
			wantCount: 1, wantJob: domain.JobNeedsReview, wantOrder: domain.OrderRefundPending,
		},
		{
			name: "免费单转失败而不是待退款", status: domain.JobPrinting, amountFen: 0,
			age: 2 * time.Hour, timed: true,
			wantCount: 1, wantJob: domain.JobNeedsReview, wantOrder: domain.OrderFailed,
		},
		{
			name: "阈值内不动", status: domain.JobPrinting, amountFen: 60,
			age: time.Minute, timed: true,
			wantCount: 0, wantJob: domain.JobPrinting, wantOrder: domain.OrderPrinting,
		},
		{
			name: "没有计时起点的任务不动", status: domain.JobPrinting, amountFen: 60,
			age: 0, timed: false,
			wantCount: 0, wantJob: domain.JobPrinting, wantOrder: domain.OrderPrinting,
		},
		{
			name: "已收尾的任务不再是候选", status: domain.JobCompleted, amountFen: 60,
			age: 2 * time.Hour, timed: true,
			wantCount: 0, wantJob: domain.JobCompleted, wantOrder: domain.OrderPrinting,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newPaymentTestEnv(t)
			order, _ := e.seedUserOrder("stuck", e.siteA, tt.amountFen,
				domain.OrderPrinting, e.now.Add(-time.Minute).Unix(), e.now.Add(10*time.Minute).Unix())
			job := e.createJob("job-stuck", order.ID, tt.status, 1, 5, 0)
			if tt.timed {
				e.setInflight(job.ID, e.now.Add(-tt.age))
			}

			count, err := e.svc.Jobs.EscalateStuckJobs(e.ctx(), 20)
			if err != nil {
				t.Fatalf("EscalateStuckJobs() 报错 = %v, want nil", err)
			}
			if count != tt.wantCount {
				t.Errorf("EscalateStuckJobs() 升级 %d 条, want %d", count, tt.wantCount)
			}
			if got := e.jobStatus(order.ID); got != string(tt.wantJob) {
				t.Errorf("任务状态 = %s, want %s", got, tt.wantJob)
			}
			if got := e.order(order.ID).Status; got != string(tt.wantOrder) {
				t.Errorf("订单状态 = %s, want %s", got, tt.wantOrder)
			}
		})
	}
}

func TestEscalateStuckJobLeavesAFinishedJobAlone(t *testing.T) {
	e := newPaymentTestEnv(t)
	order, _ := e.seedUserOrder("cas", e.siteA, 60, domain.OrderPrinting,
		e.now.Add(-time.Minute).Unix(), e.now.Add(10*time.Minute).Unix())
	job := e.createJob("job-cas", order.ID, domain.JobPrinting, 1, 5, 0)
	e.setInflight(job.ID, e.now.Add(-2*time.Hour))

	stale := job
	e.mustExec(`UPDATE print_jobs SET status = 'completed' WHERE id = ?`, job.ID)

	escalated, err := e.svc.Jobs.escalateStuckJob(e.ctx(), stale, time.Hour, e.now)
	if err != nil {
		t.Fatalf("escalateStuckJob() 报错 = %v, want nil", err)
	}
	if escalated {
		t.Error("已经收尾的任务不应被升级")
	}
	if got := e.jobStatus(order.ID); got != string(domain.JobCompleted) {
		t.Errorf("任务状态 = %s, want %s", got, domain.JobCompleted)
	}
	if got := e.order(order.ID).Status; got != string(domain.OrderPrinting) {
		t.Errorf("订单被误推进 = %s, want %s", got, domain.OrderPrinting)
	}
}

func TestEscalateStuckJobsDisabledByZeroTTL(t *testing.T) {
	e := newPaymentTestEnv(t)
	order, _ := e.seedUserOrder("off", e.siteA, 60, domain.OrderPrinting,
		e.now.Add(-time.Minute).Unix(), e.now.Add(10*time.Minute).Unix())
	job := e.createJob("job-off", order.ID, domain.JobPrinting, 1, 5, 0)
	e.setInflight(job.ID, e.now.Add(-48*time.Hour))

	if _, err := e.svc.Settings.Update(e.ctx(), UpdateSettingsInput{
		Set: map[string]string{SettingStuckJobTTL: "0s"},
	}); err != nil {
		t.Fatalf("关闭 stuck_job_ttl: %v", err)
	}

	count, err := e.svc.Jobs.EscalateStuckJobs(e.ctx(), 20)
	if err != nil {
		t.Fatalf("EscalateStuckJobs() 报错 = %v, want nil", err)
	}
	if count != 0 {
		t.Errorf("关闭后升级 %d 条, want 0", count)
	}
	if got := e.jobStatus(order.ID); got != string(domain.JobPrinting) {
		t.Errorf("任务状态 = %s, want %s", got, domain.JobPrinting)
	}
	stored, err := e.svc.Q.GetJobByID(e.ctx(), job.ID)
	if err != nil {
		t.Fatalf("读取打印任务 %s: %v", job.ID, err)
	}
	if stored.LastError != "" {
		t.Errorf("关闭时不应写入原因, got %q", stored.LastError)
	}
}
