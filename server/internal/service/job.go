package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/Sparkzure/PayPrint/server/internal/domain"
	"github.com/Sparkzure/PayPrint/server/internal/ids"
	"github.com/Sparkzure/PayPrint/server/internal/ipp"
	"github.com/Sparkzure/PayPrint/server/internal/notifier"
	"github.com/Sparkzure/PayPrint/server/internal/store"
)

type JobService struct{ *Services }

type DispatchResult struct {
	Claimed   int
	Submitted int
	Retried   int
	Failed    int
	Skipped   int

	Deferred int
}

func (svc *JobService) enqueue(
	ctx context.Context, q *store.Queries, order store.Order, now time.Time,
) (store.PrintJob, error) {
	job, err := q.CreatePrintJob(ctx, store.CreatePrintJobParams{
		ID:          ids.New(),
		OrderID:     order.ID,
		PrinterID:   order.PrinterID,
		MaxAttempts: int64(svc.Runtime().IPPMaxAttempts),
		NextRetryAt: now.Unix(),
		CreatedAt:   now.Unix(),
		UpdatedAt:   now.Unix(),
	})
	if err != nil {

		if isUniqueViolation(err) {
			return store.PrintJob{}, nil
		}
		return store.PrintJob{}, domain.ErrInternal.WithErr(err)
	}
	return job, nil
}

func (svc *JobService) DispatchDue(ctx context.Context, limit int) (DispatchResult, error) {
	var stats DispatchResult
	if limit <= 0 {
		limit = svc.Cfg.Worker.Batch
	}
	if limit <= 0 {
		limit = 20
	}
	now := svc.now()

	jobs, err := svc.Q.ListDueJobs(ctx, store.ListDueJobsParams{
		NextRetryAt: now.Unix(),
		Limit:       int64(limit),
	})
	if err != nil {
		return stats, domain.ErrInternal.WithErr(err)
	}

	for i := range jobs {
		job := jobs[i]
		claimed, err := svc.Q.ClaimJob(ctx, store.ClaimJobParams{
			LockedAt:  now.Unix(),
			LockedBy:  svc.workerID(),
			UpdatedAt: now.Unix(),
			ID:        job.ID,
		})
		if err != nil {
			svc.Log.ErrorContext(ctx, "认领打印任务失败", "job_id", job.ID, "error", err)
			continue
		}
		if claimed == 0 {

			stats.Skipped++
			continue
		}
		stats.Claimed++

		switch svc.dispatchOne(ctx, job) {
		case dispatchSubmitted:
			stats.Submitted++
		case dispatchRetried:
			stats.Retried++
		case dispatchFailed:
			stats.Failed++
		case dispatchDeferred:
			stats.Deferred++
		}
	}
	return stats, nil
}

type dispatchOutcome int

const (
	dispatchSkipped dispatchOutcome = iota
	dispatchSubmitted
	dispatchRetried
	dispatchFailed

	dispatchDeferred
)

func (svc *JobService) dispatchOne(ctx context.Context, job store.PrintJob) dispatchOutcome {
	order, err := svc.Q.GetOrderByID(ctx, job.OrderID)
	if err != nil {
		svc.Log.ErrorContext(ctx, "加载订单失败", "order_id", job.OrderID, "error", err)
		return svc.failJob(ctx, job, order, fmt.Errorf("加载订单: %w", err), false)
	}
	printer, err := svc.Q.GetPrinterByID(ctx, job.PrinterID)
	if err != nil {
		svc.Log.ErrorContext(ctx, "加载打印机失败", "printer_id", job.PrinterID, "error", err)
		return svc.failJob(ctx, job, order, fmt.Errorf("加载打印机: %w", err), false)
	}

	if !InOnlineWindow(ParseOnlineWindows(printer.OnlineWindows), svc.now()) {
		return svc.deferOutsideWindow(ctx, job, printer)
	}
	file, err := svc.Q.GetPrintFileForUser(ctx, store.GetPrintFileForUserParams{
		ID:     order.FileID,
		UserID: order.UserID,
	})
	if err != nil {

		svc.Log.ErrorContext(ctx, "加载待打印文件失败", "order_id", order.ID, "error", err)
		return svc.failJob(ctx, job, order, fmt.Errorf("加载文件: %w", err), false)
	}

	handle, err := svc.Files.Open(file)
	if err != nil {
		svc.Log.ErrorContext(ctx, "打开文件失败", "path", file.StoragePath, "error", err)
		return svc.failJob(ctx, job, order, fmt.Errorf("打开文件: %w", err), false)
	}
	defer handle.Close()

	params, ranges, err := decodeOrderPrintJob(order, file)
	if err != nil {
		return svc.failJob(ctx, job, order, err, false)
	}

	callCtx, cancel := context.WithTimeout(ctx, svc.Runtime().IPPTimeout)
	defer cancel()

	info, err := svc.IPP.PrintJob(callCtx, ipp.PrintRequest{
		PrinterURI:     printer.CupsUri,
		JobName:        jobName(order),
		DocumentFormat: svc.Cfg.IPP.JobFormat,
		Params:         params,
		PageRanges:     ranges,
		Document:       handle,
	})
	if err != nil {
		transient := ipp.IsTransient(err)
		svc.Log.WarnContext(ctx, "IPP 投递失败",
			"order_no", order.OrderNo, "transient", transient, "error", err)
		return svc.failJob(ctx, job, order, err, transient)
	}

	now := svc.nowUnix()
	state := info.State
	if state == "" {
		state = "pending"
	}
	if _, err := svc.Q.MarkJobSubmitted(ctx, store.MarkJobSubmittedParams{
		JobState:      state,
		CupsJobID:     info.ID,
		IppJobUri:     info.URI,
		SubmittedAt:   now,
		InflightSince: now,
		UpdatedAt:     now,
		ID:            job.ID,
	}); err != nil {
		svc.Log.ErrorContext(ctx, "更新任务为已提交失败", "job_id", job.ID, "error", err)
	}

	if domain.OrderStatus(order.Status) == domain.OrderQueued {
		if err := svc.Q.UpdateOrderStatus(ctx, store.UpdateOrderStatusParams{
			Status:    string(domain.OrderPrinting),
			UpdatedAt: now,
			ID:        order.ID,
		}); err != nil {
			svc.Log.WarnContext(ctx, "更新订单状态为打印中失败", "order_id", order.ID, "error", err)
		}
	}

	svc.Log.InfoContext(ctx, "打印任务已投递",
		"order_no", order.OrderNo, "cups_job_id", info.ID, "job_state", state)
	return dispatchSubmitted
}

func (svc *JobService) deferOutsideWindow(
	ctx context.Context, job store.PrintJob, printer store.Printer,
) dispatchOutcome {
	now := svc.now()
	next := svc.Printers.NextOpenAt(printer, now)
	if !next.After(now) {

		next = now.Add(time.Minute)
	}
	note := "打印机不在在线时段，已安排在 " + next.Format("01-02 15:04") + " 之后投递"
	if _, err := svc.Q.DeferJobOutsideWindow(ctx, store.DeferJobOutsideWindowParams{
		LastError:   note,
		NextRetryAt: next.Unix(),
		UpdatedAt:   now.Unix(),
		ID:          job.ID,
	}); err != nil {
		svc.Log.ErrorContext(ctx, "顺延时段外任务失败", "job_id", job.ID, "error", err)
	}
	svc.Log.InfoContext(ctx, "打印机不在在线时段，任务顺延",
		"job_id", job.ID, "printer_code", printer.Code, "next_retry_at", next)
	return dispatchDeferred
}

func (svc *JobService) failJob(
	ctx context.Context, job store.PrintJob, order store.Order, cause error, transient bool,
) dispatchOutcome {
	now := svc.now()

	attempts := job.Attempts + 1
	errText := truncate(cause.Error(), 512)

	if transient && attempts < job.MaxAttempts {
		next := now.Add(svc.backoff(attempts))
		if _, err := svc.Q.MarkJobRetry(ctx, store.MarkJobRetryParams{
			LastError:   errText,
			NextRetryAt: next.Unix(),
			UpdatedAt:   now.Unix(),
			ID:          job.ID,
		}); err != nil {
			svc.Log.ErrorContext(ctx, "重置任务为待重试失败", "job_id", job.ID, "error", err)
		}
		svc.Log.WarnContext(ctx, "打印任务稍后重试",
			"job_id", job.ID, "attempts", attempts, "next_retry_at", next)
		return dispatchRetried
	}

	status := domain.JobFailed
	if transient {

		status = domain.JobNeedsReview
	}

	if err := svc.InTx(ctx, func(q *store.Queries) error {
		if _, err := q.MarkJobFailed(ctx, store.MarkJobFailedParams{
			Status:     string(status),
			LastError:  errText,
			FinishedAt: now.Unix(),
			UpdatedAt:  now.Unix(),
			ID:         job.ID,
		}); err != nil {
			return domain.ErrInternal.WithErr(err)
		}
		return svc.escalateOrderTx(ctx, q, order, status, errText, now)
	}); err != nil {
		svc.Log.ErrorContext(ctx, "打印任务收尾失败，任务与订单状态都未变更",
			"job_id", job.ID, "order_id", order.ID, "error", err)
	}
	return dispatchFailed
}

func (svc *JobService) escalateOrderTx(
	ctx context.Context, q *store.Queries, order store.Order,
	jobStatus domain.JobStatus, reason string, now time.Time,
) error {
	next := domain.OrderFailed
	if order.AmountFen > 0 {
		next = domain.OrderRefundPending
	}

	if err := q.UpdateOrderStatus(ctx, store.UpdateOrderStatusParams{
		Status:    string(next),
		UpdatedAt: now.Unix(),
		ID:        order.ID,
	}); err != nil {
		return domain.ErrInternal.WithErr(err)
	}

	svc.Log.WarnContext(ctx, "打印任务收尾，订单转入终态",
		"order_no", order.OrderNo, "job_status", jobStatus, "order_status", next, "reason", reason)
	return nil
}

func (svc *JobService) RecoverOrphans(ctx context.Context, limit int) (int, error) {
	lease := svc.Cfg.Worker.Lease
	if lease <= 0 {
		lease = 2 * time.Minute
	}
	if limit <= 0 {
		limit = svc.Cfg.Worker.Batch
	}
	if limit <= 0 {
		limit = 20
	}

	now := svc.now()
	rows, err := svc.Q.ListOrphanedJobs(ctx, store.ListOrphanedJobsParams{
		LockedAt: now.Add(-lease).Unix(),
		Limit:    int64(limit),
	})
	if err != nil {
		return 0, domain.ErrInternal.WithErr(err)
	}

	recovered := 0
	for _, job := range rows {
		if err := svc.recoverOrphan(ctx, job, now); err != nil {
			svc.Log.ErrorContext(ctx, "回收被中断的打印任务失败",
				"job_id", job.ID, "order_id", job.OrderID, "error", err)
			continue
		}
		recovered++
	}
	return recovered, nil
}

func (svc *JobService) recoverOrphan(ctx context.Context, job store.PrintJob, now time.Time) error {

	if job.Attempts >= job.MaxAttempts {
		return svc.escalateOrphan(ctx, job, now)
	}

	reason := fmt.Sprintf("投递中断（进程在认领任务后退出），已自动重新排队（第 %d 次尝试）",
		job.Attempts+1)
	affected, err := svc.Q.RecoverOrphanedJob(ctx, store.RecoverOrphanedJobParams{
		LastError:   truncate(reason, 512),
		NextRetryAt: now.Unix(),
		UpdatedAt:   now.Unix(),
		ID:          job.ID,
		LockedAt:    job.LockedAt,
	})
	if err != nil {
		return domain.ErrInternal.WithErr(err)
	}
	if affected == 0 {

		return nil
	}

	svc.Log.WarnContext(ctx, "回收被中断的打印任务，已重新排队",
		"job_id", job.ID, "order_id", job.OrderID, "attempts", job.Attempts)
	return nil
}

func (svc *JobService) escalateOrphan(ctx context.Context, job store.PrintJob, now time.Time) error {
	reason := "投递多次中断（进程在认领任务后退出），已放弃自动重试"

	order, err := svc.Q.GetOrderByID(ctx, job.OrderID)
	if err != nil {
		return domain.ErrInternal.WithErr(err)
	}

	err = svc.InTx(ctx, func(q *store.Queries) error {
		affected, err := q.EscalateOrphanedJob(ctx, store.EscalateOrphanedJobParams{
			LastError:  truncate(reason, 512),
			FinishedAt: now.Unix(),
			UpdatedAt:  now.Unix(),
			ID:         job.ID,
			LockedAt:   job.LockedAt,
		})
		if err != nil {
			return domain.ErrInternal.WithErr(err)
		}
		if affected == 0 {

			return nil
		}
		return svc.escalateOrderTx(ctx, q, order, domain.JobNeedsReview, reason, now)
	})
	if err != nil {
		return err
	}

	svc.Log.ErrorContext(ctx, "打印任务多次中断，已转人工处理",
		"job_no", order.OrderNo, "job_id", job.ID)
	return nil
}

func (svc *JobService) EscalateStuckJobs(ctx context.Context, limit int) (int, error) {
	ttl := svc.Runtime().StuckJobTTL
	if ttl <= 0 {
		return 0, nil
	}
	if limit <= 0 {
		limit = svc.Cfg.Worker.Batch
	}
	if limit <= 0 {
		limit = 20
	}

	now := svc.now()
	jobs, err := svc.Q.ListStuckJobs(ctx, store.ListStuckJobsParams{
		InflightSince: now.Add(-ttl).Unix(),
		Limit:         int64(limit),
	})
	if err != nil {
		return 0, domain.ErrInternal.WithErr(err)
	}

	count := 0
	for _, job := range jobs {
		escalated, err := svc.escalateStuckJob(ctx, job, ttl, now)
		if err != nil {
			svc.Log.ErrorContext(ctx, "在途任务超时转人工失败",
				"job_id", job.ID, "order_id", job.OrderID, "error", err)
			continue
		}
		if escalated {
			count++
		}
	}
	return count, nil
}

func (svc *JobService) escalateStuckJob(
	ctx context.Context, job store.PrintJob, ttl time.Duration, now time.Time,
) (bool, error) {
	order, err := svc.Q.GetOrderByID(ctx, job.OrderID)
	if err != nil {
		return false, domain.ErrInternal.WithErr(err)
	}

	reason := truncate(fmt.Sprintf("任务在「%s」停留超过 %s，打印机未再报告进展",
		domain.JobStatus(job.Status).Label(), ttl), 512)

	changed := false
	err = svc.InTx(ctx, func(q *store.Queries) error {
		affected, err := q.EscalateStuckJob(ctx, store.EscalateStuckJobParams{
			LastError:  reason,
			FinishedAt: now.Unix(),
			UpdatedAt:  now.Unix(),
			ID:         job.ID,
		})
		if err != nil {
			return domain.ErrInternal.WithErr(err)
		}
		if affected == 0 {

			return nil
		}
		changed = true
		return svc.escalateOrderTx(ctx, q, order, domain.JobNeedsReview, reason, now)
	})
	if err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}

	svc.Log.ErrorContext(ctx, "在途任务长时间无进展，已转人工处理",
		"order_no", order.OrderNo, "job_id", job.ID, "job_status", job.Status,
		"inflight_since", job.InflightSince, "ttl", ttl)
	return true, nil
}

const jobGoneGrace = 30 * time.Second

func (svc *JobService) PollInFlight(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = svc.Cfg.Worker.Batch
	}
	jobs, err := svc.Q.ListJobsInFlight(ctx, int64(limit))
	if err != nil {
		return 0, domain.ErrInternal.WithErr(err)
	}

	updated := 0
	for _, job := range jobs {
		printer, err := svc.Q.GetPrinterByID(ctx, job.PrinterID)
		if err != nil {

			svc.Log.ErrorContext(ctx, "回查作业时加载打印机失败",
				"job_id", job.ID, "order_id", job.OrderID,
				"printer_id", job.PrinterID, "error", err)
			continue
		}

		callCtx, cancel := context.WithTimeout(ctx, svc.Runtime().IPPTimeout)

		info, err := svc.IPP.GetJob(callCtx, printer.CupsUri, job.IppJobUri, job.CupsJobID)
		cancel()
		if err != nil {
			if !ipp.IsJobNotFound(err) {

				svc.Log.WarnContext(ctx, "回查作业状态失败", "job_id", job.ID, "error", err)
				continue
			}

			info, err = svc.jobGone(ctx, job, printer.CupsUri, svc.now())
			if err != nil {
				svc.Log.WarnContext(ctx, "确认作业去向失败", "job_id", job.ID, "error", err)
				continue
			}
			if info == nil {

				continue
			}
		}

		code := ipp.JobStateCode(info.State)
		if code == 0 {

			svc.Log.WarnContext(ctx, "打印机没有回报可识别的 job-state",
				"job_id", job.ID, "order_id", job.OrderID,
				"cups_job_id", job.CupsJobID, "job_state", info.State,
				"state_message", info.StateMessage)
			continue
		}
		status, done := ipp.ResolveJobStatus(code)
		now := svc.nowUnix()

		if done {
			changed, err := svc.finishJobTx(ctx, job, info, status, now)
			if err != nil {
				svc.Log.ErrorContext(ctx, "收尾打印任务失败，任务与订单状态都未变更",
					"job_id", job.ID, "order_id", job.OrderID, "error", err)
				continue
			}
			if changed {
				updated++
			}
			continue
		}

		inflightSince := now
		if job.InflightSince > 0 && domain.JobStatus(job.Status) == status {
			inflightSince = job.InflightSince
		}
		affected, err := svc.Q.UpdateJobState(ctx, store.UpdateJobStateParams{
			Status:        string(status),
			JobState:      info.State,
			FinishedAt:    0,
			InflightSince: inflightSince,
			UpdatedAt:     now,
			ID:            job.ID,
		})
		if err != nil {
			svc.Log.ErrorContext(ctx, "更新作业状态失败", "job_id", job.ID, "error", err)
			continue
		}
		if affected == 0 {
			continue
		}
		updated++

		if status == domain.JobInterrupted &&
			domain.JobStatus(job.Status) != domain.JobInterrupted {
			svc.Log.ErrorContext(ctx, "打印被打印机中断，需要现场处理",
				"order_no", job.OrderID, "cups_job_id", job.CupsJobID,
				"job_state", info.State, "state_message", info.StateMessage)
		}
	}
	return updated, nil
}

func (svc *JobService) jobGone(
	ctx context.Context, job store.PrintJob, cupsURI string, now time.Time,
) (*ipp.JobInfo, error) {

	if job.SubmittedAt > 0 && now.Unix()-job.SubmittedAt < int64(jobGoneGrace.Seconds()) {
		return nil, nil
	}

	callCtx, cancel := context.WithTimeout(ctx, svc.Runtime().IPPTimeout)
	defer cancel()
	jobs, err := svc.IPP.GetJobs(callCtx, cupsURI, "not-completed")
	if err != nil {
		svc.Log.WarnContext(ctx, "打印机不支持或未能列出作业，按「作业已出队即完成」推断",
			"job_id", job.ID, "cups_job_id", job.CupsJobID, "error", err)
		return inferredCompleted(job), nil
	}

	for i := range jobs {
		if job.CupsJobID > 0 && jobs[i].ID == job.CupsJobID {

			return &jobs[i], nil
		}
	}

	svc.Log.WarnContext(ctx, "作业已不在打印机的活跃队列中，按完成收尾",
		"job_id", job.ID, "order_id", job.OrderID, "cups_job_id", job.CupsJobID)
	return inferredCompleted(job), nil
}

func inferredCompleted(job store.PrintJob) *ipp.JobInfo {
	return &ipp.JobInfo{
		ID:           job.CupsJobID,
		State:        ipp.JobStateKeyword(ipp.JobStateCompleted),
		StateMessage: "作业已从打印机队列消失（完成后被清理），按完成收尾",
	}
}

func (svc *JobService) finishJobTx(
	ctx context.Context, job store.PrintJob, info *ipp.JobInfo, status domain.JobStatus, now int64,
) (bool, error) {
	changed := false
	err := svc.InTx(ctx, func(q *store.Queries) error {
		affected, err := q.UpdateJobState(ctx, store.UpdateJobStateParams{
			Status:     string(status),
			JobState:   info.State,
			FinishedAt: now,
			UpdatedAt:  now,
			ID:         job.ID,
		})
		if err != nil {
			return domain.ErrInternal.WithErr(err)
		}
		if affected == 0 {
			return nil
		}
		changed = true

		order, err := q.GetOrderByID(ctx, job.OrderID)
		if err != nil {
			return domain.ErrInternal.WithErr(err)
		}

		if domain.OrderStatus(order.Status).IsTerminal() {
			return nil
		}

		switch status {
		case domain.JobCompleted:

			completed, err := q.MarkOrderCompleted(ctx, store.MarkOrderCompletedParams{
				CompletedAt: now,
				UpdatedAt:   now,
				ID:          order.ID,
			})
			if err != nil {
				return domain.ErrInternal.WithErr(err)
			}
			if completed == 0 {
				svc.Log.WarnContext(ctx, "订单已不在打印中，跳过完成标记",
					"order_no", order.OrderNo, "status", order.Status)
			}
			return nil
		case domain.JobFailed:
			reason := info.StateMessage
			if reason == "" {
				reason = "作业被取消或中止（" + info.State + "）"
			}
			return svc.escalateOrderTx(ctx, q, order, status, reason, time.Unix(now, 0))
		default:
			return nil
		}
	})
	if err != nil {
		return false, err
	}
	if changed && status == domain.JobCompleted {
		svc.Log.InfoContext(ctx, "打印已完成",
			"job_id", job.ID, "order_id", job.OrderID, "cups_job_id", job.CupsJobID)
	}
	return changed, nil
}

func (svc *JobService) ReconcileStrandedOrders(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = svc.Cfg.Worker.Batch
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := svc.Q.ListStrandedOrders(ctx, int64(limit))
	if err != nil {
		return 0, domain.ErrInternal.WithErr(err)
	}

	fixed := 0
	for _, row := range rows {
		order, err := svc.Q.GetOrderByID(ctx, row.OrderID)
		if err != nil {
			continue
		}
		if domain.OrderStatus(order.Status).IsTerminal() {
			continue
		}

		jobStatus := domain.JobStatus(row.JobStatus)
		now := svc.now()
		err = svc.InTx(ctx, func(q *store.Queries) error {
			if jobStatus == domain.JobCompleted {
				_, err := q.MarkOrderCompleted(ctx, store.MarkOrderCompletedParams{
					CompletedAt: now.Unix(),
					UpdatedAt:   now.Unix(),
					ID:          order.ID,
				})
				return err
			}
			reason := row.JobLastError
			if reason == "" {
				reason = "打印任务已收尾（" + jobStatus.Label() + "），订单补推进"
			}
			return svc.escalateOrderTx(ctx, q, order, jobStatus, reason, now)
		})
		if err != nil {
			svc.Log.ErrorContext(ctx, "补推进卡住的订单失败",
				"order_id", order.ID, "job_status", row.JobStatus, "error", err)
			continue
		}
		fixed++
		svc.Log.WarnContext(ctx, "打印任务已收尾但订单仍在途，已补推进",
			"order_no", order.OrderNo, "job_status", row.JobStatus,
			"order_status", order.Status)
	}
	return fixed, nil
}

func (svc *JobService) RunVerifications(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = svc.Cfg.Worker.Batch
	}
	now := svc.now()
	rows, err := svc.Q.ListDueVerifications(ctx, store.ListDueVerificationsParams{
		NextRetryAt: now.Unix(),
		Limit:       int64(limit),
	})
	if err != nil {
		return 0, domain.ErrInternal.WithErr(err)
	}

	processed := 0
	for _, row := range rows {
		claimed, err := svc.Q.ClaimVerification(ctx, store.ClaimVerificationParams{
			UpdatedAt: now.Unix(),
			ID:        row.ID,
		})
		if err != nil || claimed == 0 {
			continue
		}
		svc.verifyOne(ctx, row)
		processed++
	}
	return processed, nil
}

func (svc *JobService) verifyOne(ctx context.Context, row store.OrderVerification) {
	order, err := svc.Q.GetOrderByID(ctx, row.OrderID)
	if err != nil {
		svc.finishVerification(ctx, row, "order_missing", "订单不存在", "")
		return
	}

	querier := svc.Queriers.For(row.Channel)
	callCtx, cancel := context.WithTimeout(ctx, svc.Cfg.WeChat.Timeout)
	result, err := querier.Query(callCtx, order.OrderNo, row.OutTradeNo)
	cancel()
	if err != nil {
		svc.retryVerification(ctx, row, err)
		return
	}

	switch result.State {
	case notifier.QueryPaid:
		if result.AmountFen != 0 && result.AmountFen != order.AmountFen {
			reason := fmt.Sprintf("官方查单金额 %d 分与订单 %d 分不一致", result.AmountFen, order.AmountFen)
			svc.finishVerification(ctx, row, "amount_mismatch", reason, result.Raw)
			svc.flagDisputed(ctx, order, reason)
			return
		}
		svc.finishVerification(ctx, row, "confirmed", "", result.Raw)

	case notifier.QueryUnknown:

		if row.Attempts+1 >= row.MaxAttempts {
			svc.finishVerification(ctx, row, "unverifiable", "达到最大查单次数仍未获得明确结论", result.Raw)
			return
		}
		svc.retryVerification(ctx, row, errors.New("查单未返回明确结论: "+result.TradeState))

	default:
		reason := fmt.Sprintf("官方查单结论为 %s（trade_state=%s），与设备上报矛盾",
			result.State, result.TradeState)
		svc.finishVerification(ctx, row, "mismatch", reason, result.Raw)
		svc.flagDisputed(ctx, order, reason)
	}
}

func (svc *JobService) flagDisputed(ctx context.Context, order store.Order, reason string) {
	svc.markOrderDisputed(ctx, order, reason)
}

func (svc *JobService) finishVerification(
	ctx context.Context, row store.OrderVerification, status, lastErr, raw string,
) {
	if _, err := svc.Q.FinishVerification(ctx, store.FinishVerificationParams{
		Status:    status,
		Result:    truncate(raw, 4096),
		LastError: truncate(lastErr, 512),
		UpdatedAt: svc.nowUnix(),
		ID:        row.ID,
	}); err != nil {
		svc.Log.WarnContext(ctx, "结束查单任务失败", "verification_id", row.ID, "error", err)
	}
}

func (svc *JobService) retryVerification(ctx context.Context, row store.OrderVerification, cause error) {
	attempts := row.Attempts + 1
	if attempts >= row.MaxAttempts {
		svc.finishVerification(ctx, row, "unverifiable", cause.Error(), "")
		return
	}
	if _, err := svc.Q.MarkVerificationRetry(ctx, store.MarkVerificationRetryParams{
		LastError:   truncate(cause.Error(), 512),
		NextRetryAt: svc.now().Add(svc.backoff(attempts)).Unix(),
		UpdatedAt:   svc.nowUnix(),
		ID:          row.ID,
	}); err != nil {
		svc.Log.WarnContext(ctx, "重排查单任务失败", "verification_id", row.ID, "error", err)
	}
}

func (svc *JobService) RetryOrder(ctx context.Context, orderID, actorID string) error {
	job, err := svc.Q.GetJobByOrderID(ctx, orderID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrNotFound.WithMessage("该订单还没有打印任务")
		}
		return domain.ErrInternal.WithErr(err)
	}

	switch domain.JobStatus(job.Status) {
	case domain.JobPending, domain.JobFailed, domain.JobNeedsReview:

	case domain.JobCancelled:
		return domain.ErrConflict.WithMessage(
			"该打印任务已随订单退款被取消，如需继续打印请重新下单")
	case domain.JobSubmitting:
		return domain.ErrConflict.WithMessage(
			"任务正在投递中；如果它是进程中断留下的孤儿，后台会在租约到期后自动回收")
	default:
		return domain.ErrConflict.WithMessage(fmt.Sprintf(
			"任务当前为「%s」，已在投递或已交给打印机，重推会造成重复出纸",
			domain.JobStatus(job.Status).Label()))
	}

	now := svc.nowUnix()

	affected, err := svc.Q.RetryStuckJob(ctx, store.RetryStuckJobParams{
		LastError:   job.LastError,
		NextRetryAt: now,
		UpdatedAt:   now,
		ID:          job.ID,
	})
	if err != nil {
		return domain.ErrInternal.WithErr(err)
	}
	if affected == 0 {
		return domain.ErrConflict.WithMessage("任务状态刚刚变化，请刷新后再试")
	}

	svc.Log.InfoContext(ctx, "人工重推打印任务",
		"job_id", job.ID, "order_id", job.OrderID, "actor", actorID)
	return nil
}

func (svc *JobService) Cleanup(ctx context.Context) error {
	now := svc.nowUnix()
	if _, err := svc.Q.DeleteExpiredNonces(ctx, now); err != nil {
		return domain.ErrInternal.WithErr(err)
	}
	if _, err := svc.Q.DeleteExpiredSessions(ctx, now); err != nil {
		return domain.ErrInternal.WithErr(err)
	}
	return nil
}

func (svc *JobService) backoff(attempt int64) time.Duration {
	base := svc.Cfg.IPP.RetryBase
	if base <= 0 {
		base = 30 * time.Second
	}
	max := svc.Cfg.IPP.RetryMax
	if max <= 0 {
		max = 10 * time.Minute
	}
	d := base
	for i := int64(1); i < attempt; i++ {
		d *= 2
		if d >= max {
			return max
		}
	}
	if d > max {
		d = max
	}
	return d
}

func (svc *JobService) workerID() string {
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	return fmt.Sprintf("%s/%d", host, os.Getpid())
}

func decodeOrderPrintJob(order store.Order, file store.PrintFile) (domain.PrintParams, []domain.PageRange, error) {
	var params domain.PrintParams
	if err := json.Unmarshal([]byte(order.ParamsJson), &params); err != nil {
		return params, nil, fmt.Errorf("解析订单打印参数: %w", err)
	}

	var cost domain.CostBreakdown
	if err := json.Unmarshal([]byte(order.CostJson), &cost); err != nil {
		return params, nil, fmt.Errorf("解析订单计价快照: %w", err)
	}

	ranges := cost.PageRanges
	if domain.FilePageCountSource(file.PageCountSource).IsTrusted() &&
		coversAllPages(ranges, int(file.PageCount)) {
		ranges = nil
	}
	return params, ranges, nil
}

func coversAllPages(ranges []domain.PageRange, total int) bool {
	if len(ranges) == 0 || total <= 0 {
		return true
	}
	if len(ranges) != 1 {
		return false
	}
	return ranges[0].From == 1 && ranges[0].To == total
}

func jobName(order store.Order) string {
	name := order.OrderNo
	if order.Title != "" {
		name = order.OrderNo + " " + order.Title
	}

	return truncate(name, 200)
}
