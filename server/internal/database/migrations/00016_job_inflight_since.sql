-- +goose Up
-- inflight_since: when the job entered its current in-flight status
-- ('submitted' / 'printing' / 'interrupted'), 0 for a job that is not in flight.
--
-- Why a separate column: updated_at moves on every poll, so a job whose printer
-- went silent looks freshly touched forever. Without this timestamp nothing can
-- tell "polled a second ago" from "nobody has heard from this printer in an hour",
-- and a job that reached the printer and then vanished stays in flight until a
-- human notices the UI never finished. The escalation in JobService.EscalateStuckJobs
-- reads exactly this column.
ALTER TABLE print_jobs ADD COLUMN inflight_since INTEGER NOT NULL DEFAULT 0;

-- Existing rows: the best available approximation. submitted_at is when the job
-- left for the printer; updated_at is when anything last touched it.
UPDATE print_jobs
SET inflight_since = CASE
        WHEN submitted_at > 0 THEN submitted_at
        WHEN updated_at > 0   THEN updated_at
        ELSE created_at
    END
WHERE status IN ('submitted', 'printing', 'interrupted');

-- +goose Down
ALTER TABLE print_jobs DROP COLUMN inflight_since;
