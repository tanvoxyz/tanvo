# Tanvo Benchmarks

> English | [简体中文](benchmarks.zh-CN.md)

> Related: [Architecture](architecture.md) · [Development](development.md) · [Testing](testing.md) · [Roadmap](roadmap.md) · [AGENTS.md](../AGENTS.md)

Measurements behind design decisions, kept so that a decision can be checked again when its assumptions change. Each section says what was asked, how it was measured, what was found, what the measurement does not cover and how to run it again. The numbers are from one machine and vary by ±30% between runs; read them as orders of magnitude and ratios, not as capacities.

---

## 1. Job Queue: River and a Redis Queue

Measured on 2026-10-26 with River v0.49.0. The tool is [`tools/queuebench`](../tools/queuebench).

### 1.1 Question

Does River, a PostgreSQL queue, limit the background jobs (§9 of the [architecture](architecture.md#9-jobs-and-scheduling-libsjobs)), and would a Redis-backed queue do better? Tanvo enqueues every job in the transaction of the state change that needs it, so a Redis queue that kept this guarantee would need an outbox.

### 1.2 Method

Every system runs the same workload. A producer transaction inserts a business row and enqueues a job for it; a worker runs the job, which does nothing (`noop`) or updates that row (`pg`, one more PostgreSQL write, as most of Tanvo's jobs do). The latency of a job runs from its enqueue to the end of its work.

| System | Enqueue | Guarantee |
| :--- | :--- | :--- |
| `river` | River `InsertTx` in the producer's transaction, as Tanvo enqueues | Transactional |
| `stream` | Commit, then `XADD` to a Valkey stream; consumers in a group with `XREADGROUP` and `XACK` | None: a crash between the commit and the `XADD` loses the job. An upper bound for Redis |
| `outbox` | An outbox row in the producer's transaction; 4 relays move batches of 500 to the stream (`DELETE … FOR UPDATE SKIP LOCKED RETURNING`, then pipelined `XADD`s) | Transactional, at least once: what a Redis queue keeping Tanvo's guarantee costs |

Two modes: `max` enqueues 50,000 jobs (30,000 for River's defaults) with 32 producers as fast as possible; `rate` enqueues at a fixed rate for 20 seconds. Unless stated, 50 workers, one River queue, a pool of 100 connections. `cd` is River's fetch cooldown (`FetchCooldown`); `W` the workers, `P` the producers.

Environment: one MacBook (Apple silicon, 10 cores), Docker on OrbStack; `postgres:18` with `max_connections=300` and `shared_buffers=1GB`; `valkey/valkey:8.1.10` with AOF and `appendfsync everysec`; the benchmark in one process on the host.

### 1.3 Results

Jobs that do nothing (end-to-end rate and latency):

| System | Jobs/s | p50 | p99 |
| :--- | ---: | ---: | ---: |
| River, defaults (cd 100 ms, W 50) | 499 | 27.6 s | 54.7 s |
| River, cd 50 ms | 993 | 20.6 s | 41.1 s |
| River, cd 25 ms | 1,979 | 8.2 s | 16.2 s |
| River, cd 10 ms | 4,248 | 1.4 s | 2.7 s |
| River, cd 25 ms, W 200 | 4,572 | 26 ms | 85 ms |
| Stream | 3,541 | 1.3 ms | 3.7 ms |
| Outbox | 3,388 | 11.1 ms | 295 ms |

Jobs that update their row:

| System | Jobs/s | p50 | p99 |
| :--- | ---: | ---: | ---: |
| River, defaults | 488 | 27.5 s | 52.3 s |
| River, cd 10 ms | 2,032 | 5.1 s | 7.3 s |
| River, cd 25 ms, W 200 | 2,845 | 35 ms | 247 ms |
| Stream | 3,430 | 5.1 ms | 48 ms |
| Outbox | 3,498 | 17.3 ms | 377 ms |
| River, cd 25 ms, W 200, P 96 | 3,159 | 0.8 s | 1.2 s |
| Stream, P 96 | 3,730 | 1.7 s | 3.9 s |
| Outbox, P 96 | 3,271 | 3.1 s | 4.5 s |

At a fixed rate, jobs that update their row:

| System | 500/s: done/s | p50 | p99 | 2,000/s: done/s | p50 | p99 |
| :--- | ---: | ---: | ---: | ---: | ---: | ---: |
| River, defaults | 468 | 166 ms | 1.38 s | 482 | 32.8 s | 62.2 s |
| River, cd 25 ms (W 50) | 481 | 19 ms | 73 ms | 1,552 | 2.2 s | 5.5 s |
| Stream | 496 | 6 ms | 215 ms | 1,999 | 5.8 ms | 207 ms |
| Outbox | 500 | 5.7 ms | 53 ms | 1,954 | 23 ms | 430 ms |

The raw output of every run is the last line `queuebench` prints; `run.sh` prints the whole matrix.

### 1.4 Findings

* **River's limit is its fetch, not PostgreSQL.** River fetches a queue at most once per cooldown and takes as many jobs as it has free workers, so a queue runs at most workers ÷ cooldown jobs a second in a process: 50 ÷ 100 ms = 500, 50 ÷ 50 ms = 1,000, 50 ÷ 25 ms = 2,000, as measured. Jobs that finish within a cooldown leave their workers idle until the next fetch.
* **With enough workers per fetch, River matches the Redis queues in throughput.** At 25 ms and 200 workers River ran 4,572 empty jobs a second, more than the stream's 3,541, and 2,845 jobs with a write against the stream's 3,430. Every system tops out at 3,000–4,500 jobs a second, which is what the PostgreSQL transactions that enqueue (and the jobs' own writes) sustain on this machine: Redis removes the queue's PostgreSQL writes, not the business transaction's.
* **A cooldown below 25 ms costs more than it gains.** At 10 ms the fetches contend with the inserts: with writing jobs River dropped to 2,032 jobs a second with 50 workers, below 25 ms with 200 workers.
* **Redis's advantage is latency.** The non-transactional stream runs a job about 5 ms after its enqueue, against 20–35 ms for River at 25 ms (half a cooldown on average, plus the fetch). The transactional outbox gives that advantage back: its relays batch, and its tail latency (p99 50–430 ms) is no better than River's.
* **Consequences for Tanvo.** River's 100 ms default capped each shard of the billing queue (20 workers over 4 shards, 5 per shard) at 50 jobs a second in a process, 200 for the queue. Tanvo's fetch cooldown is now 25 ms (`TANVO_JOBS_FETCH_COOLDOWN`), and `TANVO_JOBS_SHARD_CONCURRENCY` raises a queue's workers per shard without changing its other shards' share ([architecture §9](architecture.md#9-jobs-and-scheduling-libsjobs)). No Redis queue is planned: it would add a component and an outbox to save tens of milliseconds of latency that no billing job needs.

### 1.5 Not Covered

These would change the conclusion if they turned out badly; measure them before relying on River well beyond the rates above.

* **Several processes on one queue.** Each process fetches on its own, so their fetches contend on `river_job`; the benchmark ran one process.
* **Table bloat over time.** Runs lasted seconds to minutes on a freshly truncated `river_job`. Days of millions of jobs, with River's cleanup of completed jobs and autovacuum, were not run.
* **Production hardware.** Network latency between the server, PostgreSQL and Valkey, and a disk with real `fsync` costs, would lower every rate here; the ratios between the systems matter more than the rates.
* **Tanvo's jobs themselves.** The workload is one insert and one update; a billing job runs many queries, so its own work, not the queue, sets its throughput.

### 1.6 Running It Again

`tools/queuebench/run.sh` starts the PostgreSQL and Valkey containers on ports 55432 and 56379, builds the tool and runs the matrix above, one line per run; a single run is `go run ./tools/queuebench -system river -work pg -n 50000 -cooldown 25ms -workers 200` against running containers (`-h` lists the flags).

Each run first ends every other client connection to the database. Behind a port forward (OrbStack, Docker Desktop) a connection of an earlier run can outlive its process, idle in a transaction, holding locks and an old snapshot that slow every later run until the forward drops it; the containers also set `idle_in_transaction_session_timeout` to 10 seconds. Runs made before this guard measured the jobs that write three to six times slower and are not reported.
