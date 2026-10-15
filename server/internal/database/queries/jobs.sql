-- A job is only ever created after the user explicitly confirmed printing,
-- so it always starts as 'pending'. The UNIQUE constraint on order_id is the last
-- line of defence against producing two jobs for one order.
-- name: CreatePrintJob :one
INSERT INTO print_jobs (
    id, order_id, printer_id, status, attempts, max_attempts,
    next_retry_at, created_at, updated_at
) VALUES (?, ?, ?, 'pending', 0, ?, ?, ?, ?)
RETURNING *;

-- name: GetJobByOrderID :one
SELECT * FROM print_jobs WHERE order_id = ?;

-- name: GetJobByID :one
SELECT * FROM print_jobs WHERE id = ?;

-- name: ListDueJobs :many
SELECT * FROM print_jobs
WHERE status = 'pending' AND next_retry_at <= ?
ORDER BY next_retry_at
LIMIT ?;

-- Claim a job: pending -> submitting. Zero affected rows means another worker
-- took it first, so the caller must simply skip.
-- name: ClaimJob :execrows
UPDATE print_jobs
SET status = 'submitting',
    locked_at = ?,
    locked_by = ?,
    attempts = attempts + 1,
    updated_at = ?
WHERE id = ? AND status = 'pending';

-- name: MarkJobSubmitted :execrows
UPDATE print_jobs
SET status = 'submitted',
    job_state = ?,
    cups_job_id = ?,
    ipp_job_uri = ?,
    last_error = '',
    locked_at = 0,
    locked_by = '',
    submitted_at = ?,
    inflight_since = ?,
    updated_at = ?
WHERE id = ?;

-- name: MarkJobRetry :execrows
UPDATE print_jobs
SET status = 'pending',
    last_error = ?,
    next_retry_at = ?,
    locked_at = 0,
    locked_by = '',
    finished_at = 0,
    updated_at = ?
WHERE id = ?;

-- Admin-initiated re-queue ("retry this stuck job").
--
-- Guarded on purpose, and deliberately NOT the same statement the worker uses.
-- The worker calls MarkJobRetry right after ClaimJob put the job in 'submitting',
-- so it may legally touch that state; an operator must never be able to push an
-- in-flight job ('submitting' / 'submitted' / 'printing') back to 'pending',
-- because that hands the same document to CUPS a second time.
-- Zero affected rows means the job moved on and the caller must refuse.
-- attempts is reset so a manual retry gets the full retry budget back.
-- name: RetryStuckJob :execrows
UPDATE print_jobs
SET status = 'pending',
    attempts = 0,
    last_error = ?,
    next_retry_at = ?,
    locked_at = 0,
    locked_by = '',
    finished_at = 0,
    updated_at = ?
WHERE id = ?
  AND status IN ('pending', 'failed', 'needs_review');

-- Stop a job that has not been handed to CUPS yet.
--
-- Only 'pending' qualifies. 'submitting' means a worker may already be mid-call and
-- 'submitted'/'printing' mean the spooler owns it; pretending either is "cancelled"
-- would just mislead whoever has to clean up afterwards. Zero affected rows tells
-- the caller the paper is already committed.
-- name: CancelPendingJob :execrows
UPDATE print_jobs
SET status = ?,
    last_error = ?,
    finished_at = ?,
    locked_at = 0,
    locked_by = '',
    updated_at = ?
WHERE order_id = ?
  AND status = 'pending';

-- Recover jobs orphaned in 'submitting'.
--
-- ClaimJob moves a job to 'submitting' before the IPP call. If the process dies in
-- between, nothing ever moves it again: ListDueJobs only looks at 'pending' and
-- ListJobsInFlight only at 'submitted'/'printing'. The order then sits in 'queued'
-- forever -- no paper, no refund, no alert.
--
-- locked_at is the lease: only jobs abandoned for longer than the lease are
-- considered orphans, so a slow-but-alive worker is never stolen from. The
-- comparison is repeated verbatim in the recovery statements below, which makes
-- them compare-and-swap: if the original worker (or another instance) just refreshed
-- the row, our UPDATE matches nothing and we leave it alone.
-- name: ListOrphanedJobs :many
SELECT * FROM print_jobs
WHERE status = 'submitting'
  AND locked_at > 0
  AND locked_at <= ?
ORDER BY locked_at
LIMIT ?;

-- name: RecoverOrphanedJob :execrows
UPDATE print_jobs
SET status = 'pending',
    last_error = ?,
    next_retry_at = ?,
    locked_at = 0,
    locked_by = '',
    updated_at = ?
WHERE id = ?
  AND status = 'submitting'
  AND locked_at = ?;

-- An orphan that has already burned its retry budget goes to a human instead of
-- being re-queued forever.
-- name: EscalateOrphanedJob :execrows
UPDATE print_jobs
SET status = 'needs_review',
    last_error = ?,
    finished_at = ?,
    locked_at = 0,
    locked_by = '',
    updated_at = ?
WHERE id = ?
  AND status = 'submitting'
  AND locked_at = ?;

-- Park a job until the printer's online window opens again.
--
-- The dispatch loop refuses to hand a document to a printer outside its configured
-- online windows: the print point is closed, so the attempt would only fail against
-- a powered-off printer and burn the retry budget. The job goes back to 'pending'
-- with next_retry_at pointing at the next opening time.
--
-- attempts is decremented on purpose: a closed shop is not a failed delivery, and
-- counting it would make a job give up after a few nights of retries. Guarded on
-- 'submitting' (the state ClaimJob left it in) so a job that already moved on is
-- never resurrected.
-- name: DeferJobOutsideWindow :execrows
UPDATE print_jobs
SET status = 'pending',
    last_error = ?,
    next_retry_at = ?,
    attempts = CASE WHEN attempts > 0 THEN attempts - 1 ELSE 0 END,
    locked_at = 0,
    locked_by = '',
    updated_at = ?
WHERE id = ?
  AND status = 'submitting';

-- name: MarkJobFailed :execrows
UPDATE print_jobs
SET status = ?,
    last_error = ?,
    finished_at = ?,
    locked_at = 0,
    locked_by = '',
    updated_at = ?
WHERE id = ?
  AND status != 'completed';

-- 'interrupted' is included on purpose: a job stopped by the printer can resume
-- by itself once the jam is cleared, so it must stay pollable. Leaving it out
-- would strand the job (and the order) in a state nothing ever revisits.
--
-- inflight_since is passed in rather than derived here: the caller holds the row
-- it just polled, so it is the one place that knows whether the status moved.
-- A successful poll only proves the printer is answering -- it leaves the stuck
-- clock alone unless the status actually changed, so a job the printer keeps
-- calling 'printing' still ages, and a silent printer never reaches this
-- statement at all (its poll fails earlier).
-- name: UpdateJobState :execrows
UPDATE print_jobs
SET status = ?,
    job_state = ?,
    finished_at = ?,
    inflight_since = ?,
    updated_at = ?
WHERE id = ?
  AND status IN ('submitted', 'printing', 'interrupted');

-- name: ListJobsInFlight :many
SELECT * FROM print_jobs
WHERE status IN ('submitted', 'printing', 'interrupted')
ORDER BY updated_at
LIMIT ?;

-- In-flight jobs the printer has not been heard from for longer than the
-- escalation TTL.
--
-- A job reaches 'submitted' / 'printing' / 'interrupted' and then the printer
-- stops answering -- powered off, network gone, spooler reset by a reboot. Every
-- poll errors out and returns early, so the job, and with it the order, stays in
-- flight forever: the money is gone and no paper comes out. Nothing else in this
-- file covers that shape. ListDueJobs only looks at 'pending', ListJobsInFlight
-- assumes the poll is still succeeding, and ListStrandedOrders needs a job that
-- already reached a terminal state.
--
-- inflight_since is what makes the scan possible: updated_at moves on every poll,
-- so a job nobody can reach looks freshly touched.
-- name: ListStuckJobs :many
SELECT * FROM print_jobs
WHERE status IN ('submitted', 'printing', 'interrupted')
  AND inflight_since > 0
  AND inflight_since <= ?
ORDER BY inflight_since
LIMIT ?;

-- Hand a stuck job to a human.
--
-- Guarded on the same in-flight set the scan selects, which makes it
-- compare-and-swap: a job that finished between the scan and this statement (the
-- paper did come out, just late) matches nothing and is left alone. Re-queueing
-- it instead would hand the same document to the printer twice.
-- name: EscalateStuckJob :execrows
UPDATE print_jobs
SET status = 'needs_review',
    last_error = ?,
    finished_at = ?,
    updated_at = ?
WHERE id = ?
  AND status IN ('submitted', 'printing', 'interrupted');

-- Orders whose print job already reached a terminal state while the order itself
-- is still sitting in an in-flight status.
--
-- This is a leftover from the era when finishing a job and advancing its order
-- were two separate statements: a crash (or a failed second write) in between left
-- the job terminal -- and therefore out of ListJobsInFlight's scan range -- while
-- the order stayed 'queued'/'printing' forever. The paper came out; the UI never
-- finished. Reconciling only ever advances the *order*: the job must never be
-- re-queued, because that would hand the same document to the printer twice.
-- name: ListStrandedOrders :many
SELECT o.id AS order_id, j.status AS job_status, j.last_error AS job_last_error
FROM orders o
JOIN print_jobs j ON j.order_id = o.id
WHERE o.status IN ('queued', 'printing')
  AND j.status IN ('completed', 'failed', 'needs_review')
ORDER BY o.updated_at
LIMIT ?;

-- name: CountJobsByStatus :many
SELECT status, COUNT(*) AS total FROM print_jobs GROUP BY status;
