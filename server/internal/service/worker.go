package service

import (
	"context"
	"log/slog"
	"time"
)

func (s *Services) RunWorkers(ctx context.Context, log *slog.Logger) {
	interval := s.Cfg.Worker.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	batch := s.Cfg.Worker.Batch
	if batch <= 0 {
		batch = 20
	}

	pollInterval := func() time.Duration { return s.Runtime().IPPPollInterval }
	fixed := func(d time.Duration) func() time.Duration {
		return func() time.Duration { return d }
	}

	go s.loop(ctx, log, "dispatch", fixed(interval), func(ctx context.Context) {

		if recovered, err := s.Jobs.RecoverOrphans(ctx, batch); err != nil {
			log.ErrorContext(ctx, "回收被中断的打印任务出错", "error", err)
		} else if recovered > 0 {
			log.WarnContext(ctx, "已回收被中断的打印任务", "count", recovered)
		}

		stats, err := s.Jobs.DispatchDue(ctx, batch)
		if err != nil {
			log.ErrorContext(ctx, "打印派发循环出错", "error", err)
			return
		}
		if stats.Claimed > 0 {
			log.InfoContext(ctx, "打印派发完成",
				"claimed", stats.Claimed,
				"submitted", stats.Submitted,
				"retried", stats.Retried,
				"failed", stats.Failed,
				"deferred", stats.Deferred)
		}
	})

	go s.loop(ctx, log, "poll", pollInterval, func(ctx context.Context) {
		if _, err := s.Jobs.PollInFlight(ctx, batch); err != nil {
			log.ErrorContext(ctx, "作业状态回查循环出错", "error", err)
		}

		if fixed, err := s.Jobs.ReconcileStrandedOrders(ctx, batch); err != nil {
			log.ErrorContext(ctx, "补推进卡住的订单出错", "error", err)
		} else if fixed > 0 {
			log.WarnContext(ctx, "已补推进卡住的订单", "count", fixed)
		}

		if stuck, err := s.Jobs.EscalateStuckJobs(ctx, batch); err != nil {
			log.ErrorContext(ctx, "在途任务超时转人工出错", "error", err)
		} else if stuck > 0 {
			log.ErrorContext(ctx, "在途任务长时间无进展，已转人工处理", "count", stuck)
		}
	})

	go s.loop(ctx, log, "verify", pollInterval, func(ctx context.Context) {
		if _, err := s.Jobs.RunVerifications(ctx, batch); err != nil {
			log.ErrorContext(ctx, "支付兜底核验循环出错", "error", err)
		}
	})

	go s.loop(ctx, log, "housekeeping", fixed(time.Minute), func(ctx context.Context) {

		closed, err := s.Orders.ExpireStaleOrders(ctx, 200)
		if err != nil {
			log.ErrorContext(ctx, "关闭超时订单出错", "error", err)
		} else if closed > 0 {
			log.InfoContext(ctx, "已关闭超时订单", "count", closed)
		}

		recycled, err := s.Orders.ExpireStaleReleases(ctx, 200)
		if err != nil {
			log.ErrorContext(ctx, "回收待确认订单出错", "error", err)
		} else if recycled > 0 {
			log.WarnContext(ctx, "待确认打印超时，已转待退款", "count", recycled)
		}

		if err := s.Jobs.Cleanup(ctx); err != nil {
			log.ErrorContext(ctx, "清理过期数据出错", "error", err)
		}
	})

	<-ctx.Done()
}

func (s *Services) loop(
	ctx context.Context, log *slog.Logger, name string,
	interval func() time.Duration, fn func(context.Context),
) {
	next := interval()
	if next <= 0 {
		next = 5 * time.Second
	}
	timer := time.NewTimer(next)
	defer timer.Stop()

	log.DebugContext(ctx, "后台循环已启动", "loop", name, "interval", next)
	for {
		select {
		case <-ctx.Done():
			log.DebugContext(ctx, "后台循环退出", "loop", name)
			return
		case <-timer.C:
			fn(ctx)
			next = interval()
			if next <= 0 {
				next = 5 * time.Second
			}
			timer.Reset(next)
		}
	}
}
