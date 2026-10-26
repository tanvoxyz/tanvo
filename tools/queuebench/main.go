package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/valkey-io/valkey-go"
)

// config holds the flags of a run.
type config struct {
	system    string
	mode      string
	total     int
	rate      int
	dur       time.Duration
	producers int
	workers   int
	relays    int
	work      string
	conns     int
	cooldown  time.Duration
	pgURL     string
	vkAddr    string
}

const stream, group = "qb:jobs", "qb"

// Args are a job's arguments: the business row and the enqueue time.
type Args struct {
	ID int64 `json:"id"`
	At int64 `json:"at"`
}

// Kind is the River job kind.
func (Args) Kind() string { return "bench" }

type stats struct {
	mu   sync.Mutex
	lat  []time.Duration
	done atomic.Int64
}

func (s *stats) record(at int64) {
	d := time.Since(time.Unix(0, at))
	s.mu.Lock()
	s.lat = append(s.lat, d)
	s.mu.Unlock()
	s.done.Add(1)
}

func (s *stats) clear() {
	s.mu.Lock()
	s.lat = s.lat[:0]
	s.mu.Unlock()
	s.done.Store(0)
}

type bench struct {
	cfg  config
	pool *pgxpool.Pool
	vk   valkey.Client
	st   stats
	seq  atomic.Int64
}

func (b *bench) doWork(ctx context.Context, a Args) error {
	if b.cfg.work == "pg" {
		if _, err := b.pool.Exec(ctx, `UPDATE biz SET done = true WHERE id = $1`, a.ID); err != nil {
			return fmt.Errorf("work: %w", err)
		}
	}
	b.st.record(a.At)
	return nil
}

type riverWorker struct {
	river.WorkerDefaults[Args]
	b *bench
}

func (w *riverWorker) Work(ctx context.Context, j *river.Job[Args]) error {
	return w.b.doWork(ctx, j.Args)
}

func main() {
	var cfg config
	flag.StringVar(&cfg.system, "system", "river", "river | stream | outbox")
	flag.StringVar(&cfg.mode, "mode", "max", "max (as fast as possible) | rate (fixed rate)")
	flag.IntVar(&cfg.total, "n", 100000, "jobs in max mode")
	flag.IntVar(&cfg.rate, "rate", 1000, "jobs per second in rate mode")
	flag.DurationVar(&cfg.dur, "dur", 20*time.Second, "duration in rate mode")
	flag.IntVar(&cfg.producers, "producers", 32, "producer goroutines")
	flag.IntVar(&cfg.workers, "workers", 50, "worker goroutines (River MaxWorkers)")
	flag.IntVar(&cfg.relays, "relays", 4, "outbox relay goroutines")
	flag.StringVar(&cfg.work, "work", "noop", "noop | pg (one UPDATE of the row)")
	flag.IntVar(&cfg.conns, "conns", 100, "pgx pool size")
	flag.DurationVar(&cfg.cooldown, "cooldown", 0, "River FetchCooldown (0: River's default, 100ms)")
	flag.StringVar(&cfg.pgURL, "pg", "postgres://b:b@127.0.0.1:55432/b?sslmode=disable", "PostgreSQL URL")
	flag.StringVar(&cfg.vkAddr, "valkey", "127.0.0.1:56379", "Valkey address")
	flag.Parse()
	if err := run(context.Background(), cfg); err != nil {
		slog.Error("queuebench", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg config) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	pc, err := pgxpool.ParseConfig(cfg.pgURL)
	if err != nil {
		return fmt.Errorf("parse -pg: %w", err)
	}
	pc.MaxConns = int32(cfg.conns)
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	defer pool.Close()
	vk, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{cfg.vkAddr}})
	if err != nil {
		return fmt.Errorf("connect to Valkey: %w", err)
	}
	defer vk.Close()
	b := &bench{cfg: cfg, pool: pool, vk: vk}
	if err := b.reset(ctx); err != nil {
		return err
	}
	// A failing worker or relay ends the run.
	fail := func(err error) {
		if err != nil && ctx.Err() == nil {
			cancel(err)
		}
	}

	var enqueue func(context.Context) error
	stop := func() {}
	switch cfg.system {
	case "river":
		ws := river.NewWorkers()
		river.AddWorker(ws, &riverWorker{b: b})
		rc, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
			Queues:        map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: cfg.workers}},
			Workers:       ws,
			FetchCooldown: cfg.cooldown,
		})
		if err != nil {
			return fmt.Errorf("river client: %w", err)
		}
		if err := rc.Start(ctx); err != nil {
			return fmt.Errorf("start river: %w", err)
		}
		stop = func() { _ = rc.Stop(context.WithoutCancel(ctx)) }
		enqueue = func(ctx context.Context) error {
			return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
				id, err := b.row(ctx, tx)
				if err != nil {
					return err
				}
				_, err = rc.InsertTx(ctx, tx, Args{ID: id, At: time.Now().UnixNano()}, nil)
				return err
			})
		}
	case "stream":
		b.startStreamWorkers(ctx, fail)
		enqueue = func(ctx context.Context) error {
			var id int64
			if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) (err error) {
				id, err = b.row(ctx, tx)
				return err
			}); err != nil {
				return err
			}
			p, err := json.Marshal(Args{ID: id, At: time.Now().UnixNano()})
			if err != nil {
				return err
			}
			return vk.Do(ctx, vk.B().Xadd().Key(stream).Id("*").FieldValue().FieldValue("p", string(p)).Build()).Error()
		}
	case "outbox":
		b.startStreamWorkers(ctx, fail)
		for range cfg.relays {
			go b.relay(ctx)
		}
		enqueue = func(ctx context.Context) error {
			return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
				id, err := b.row(ctx, tx)
				if err != nil {
					return err
				}
				p, err := json.Marshal(Args{ID: id, At: time.Now().UnixNano()})
				if err != nil {
					return err
				}
				_, err = tx.Exec(ctx, `INSERT INTO outbox (payload) VALUES ($1)`, p)
				return err
			})
		}
	default:
		return fmt.Errorf("unknown -system %q", cfg.system)
	}
	defer stop()

	// Warm up the connections and the code paths.
	const warm = 200
	for range warm {
		if err := enqueue(ctx); err != nil {
			return fmt.Errorf("warm up: %w", err)
		}
	}
	if err := waitDone(ctx, &b.st, warm, 30*time.Second); err != nil {
		return fmt.Errorf("warm up: %w", err)
	}
	b.st.clear()

	n := cfg.total
	jobs := make(chan struct{}, 1024)
	if cfg.mode == "rate" {
		n = cfg.rate * int(cfg.dur.Seconds())
		go func() {
			defer close(jobs)
			interval := time.Second / time.Duration(cfg.rate)
			t0 := time.Now()
			for i := range n {
				time.Sleep(time.Until(t0.Add(time.Duration(i) * interval)))
				jobs <- struct{}{}
			}
		}()
	} else {
		go func() {
			defer close(jobs)
			for range n {
				jobs <- struct{}{}
			}
		}()
	}
	start := time.Now()
	var wg sync.WaitGroup
	for range cfg.producers {
		wg.Go(func() {
			for range jobs {
				if ctx.Err() != nil {
					continue
				}
				fail(enqueue(ctx))
			}
		})
	}
	wg.Wait()
	enqTime := time.Since(start)
	if err := waitDone(ctx, &b.st, int64(n), 5*time.Minute); err != nil {
		return err
	}
	all := time.Since(start)

	b.st.mu.Lock()
	lat := slices.Clone(b.st.lat)
	b.st.mu.Unlock()
	slices.Sort(lat)
	q := func(p float64) time.Duration { return lat[int(p*float64(len(lat)-1))].Round(100 * time.Microsecond) }
	fmt.Printf("%-6s %-4s work=%-4s cd=%v P=%d W=%d n=%d  enqueue %7.0f/s  end-to-end %7.0f/s  latency p50 %v p95 %v p99 %v max %v\n",
		cfg.system, cfg.mode, cfg.work, cfg.cooldown, cfg.producers, cfg.workers, n,
		float64(n)/enqTime.Seconds(), float64(n)/all.Seconds(), q(.5), q(.95), q(.99), lat[len(lat)-1].Round(time.Millisecond))
	return nil
}

func (b *bench) row(ctx context.Context, tx pgx.Tx) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `INSERT INTO biz (org, amount) VALUES ($1, $2) RETURNING id`, b.seq.Add(1)%1000, 42).Scan(&id)
	return id, err
}

func (b *bench) startStreamWorkers(ctx context.Context, fail func(error)) {
	for i := range b.cfg.workers {
		consumer := "c" + strconv.Itoa(i)
		go func() {
			for ctx.Err() == nil {
				fail(b.readStream(ctx, consumer))
			}
		}()
	}
}

// readStream runs and acknowledges the next batch of the consumer's jobs.
func (b *bench) readStream(ctx context.Context, consumer string) error {
	res, err := b.vk.Do(ctx, b.vk.B().Xreadgroup().Group(group, consumer).Count(10).Block(1000).Streams().Key(stream).Id(">").Build()).AsXRead()
	if valkey.IsValkeyNil(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("xreadgroup: %w", err)
	}
	var ids []string
	for _, e := range res[stream] {
		var a Args
		if err := json.Unmarshal([]byte(e.FieldValues["p"]), &a); err != nil {
			return err
		}
		if err := b.doWork(ctx, a); err != nil {
			return err
		}
		ids = append(ids, e.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	return b.vk.Do(ctx, b.vk.B().Xack().Key(stream).Group(group).Id(ids...).Build()).Error()
}

// relay moves outbox rows to the stream, deleting them in the transaction
// that read them once the XADDs succeeded (at least once).
func (b *bench) relay(ctx context.Context) {
	for ctx.Err() == nil {
		moved := 0
		err := pgx.BeginFunc(ctx, b.pool, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `DELETE FROM outbox WHERE id IN (SELECT id FROM outbox ORDER BY id LIMIT 500 FOR UPDATE SKIP LOCKED) RETURNING payload`)
			if err != nil {
				return err
			}
			var cmds valkey.Commands
			for rows.Next() {
				var p []byte
				if err := rows.Scan(&p); err != nil {
					return err
				}
				cmds = append(cmds, b.vk.B().Xadd().Key(stream).Id("*").FieldValue().FieldValue("p", string(p)).Build())
			}
			if err := rows.Err(); err != nil {
				return err
			}
			moved = len(cmds)
			for _, r := range b.vk.DoMulti(ctx, cmds...) {
				if err := r.Error(); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "relay", "err", err)
		}
		if moved == 0 {
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// reset empties the tables and the stream. It also ends every other client
// backend first: a previous run's connections may outlive it behind a port
// forward, idle in a transaction, holding locks and an old snapshot.
func (b *bench) reset(ctx context.Context) error {
	if _, err := b.pool.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = current_database() AND pid <> pg_backend_pid() AND backend_type = 'client backend'`); err != nil {
		return fmt.Errorf("end other backends: %w", err)
	}
	if _, err := b.pool.Exec(ctx, `
CREATE TABLE IF NOT EXISTS biz (id bigserial PRIMARY KEY, org bigint NOT NULL, amount bigint NOT NULL, done bool NOT NULL DEFAULT false, created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS outbox (id bigserial PRIMARY KEY, payload jsonb NOT NULL);
TRUNCATE biz, outbox;`); err != nil {
		return fmt.Errorf("create tables: %w", err)
	}
	m, err := rivermigrate.New(riverpgxv5.New(b.pool), nil)
	if err != nil {
		return fmt.Errorf("river migrator: %w", err)
	}
	if _, err := m.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("migrate river: %w", err)
	}
	if _, err := b.pool.Exec(ctx, `TRUNCATE river_job`); err != nil {
		return fmt.Errorf("truncate river_job: %w", err)
	}
	if _, err := b.pool.Exec(ctx, `VACUUM ANALYZE`); err != nil {
		return fmt.Errorf("vacuum: %w", err)
	}
	if err := b.vk.Do(ctx, b.vk.B().Del().Key(stream).Build()).Error(); err != nil {
		return fmt.Errorf("delete stream: %w", err)
	}
	if err := b.vk.Do(ctx, b.vk.B().XgroupCreate().Key(stream).Group(group).Id("0").Mkstream().Build()).Error(); err != nil {
		return fmt.Errorf("create group: %w", err)
	}
	return nil
}

// waitDone waits until n jobs are done, the run fails or the limit passes.
func waitDone(ctx context.Context, s *stats, n int64, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for s.done.Load() < n {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out: %d of %d jobs done", s.done.Load(), n)
		}
		time.Sleep(time.Millisecond)
	}
	return nil
}
