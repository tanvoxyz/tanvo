-- Counters (port.Sequences) and the job outbox (port.JobEnqueuer).

-- name: NextCounter :one
INSERT INTO counters (organization_id, name, scope, value)
VALUES (@organization_id, @name, @scope, 1)
ON CONFLICT (organization_id, name, scope) DO UPDATE SET value = counters.value + 1
RETURNING value;

-- name: InsertJob :execrows
INSERT INTO job_outbox (organization_id, kind, queue, args, run_at, unique_key, max_attempts)
VALUES (@organization_id, @kind, @queue, @args, @run_at, @unique_key, @max_attempts)
ON CONFLICT DO NOTHING;

-- name: ListJobs :many
SELECT * FROM job_outbox ORDER BY id;

-- name: TakeJobs :many
DELETE FROM job_outbox
WHERE id IN (SELECT o.id FROM job_outbox o ORDER BY o.id LIMIT @lim FOR UPDATE SKIP LOCKED)
RETURNING *;
