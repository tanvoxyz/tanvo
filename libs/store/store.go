package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Store is the PostgreSQL implementation of [port.Transactor].
type Store struct {
	pool *pgxpool.Pool
	rls  bool
	jobs JobInserter
}

var _ port.Transactor = (*Store)(nil)

// Option configures a [Store].
type Option func(*Store)

// WithRowLevelSecurity makes every transaction set tanvo.organization_id
// before it touches an organization's rows, so that the row-level security
// policies admit them when the connection's role does not bypass RLS
// (Tanvo Cloud). Without it the role must own the tables (self-hosted).
func WithRowLevelSecurity() Option {
	return func(s *Store) { s.rls = true }
}

// JobInserter inserts jobs inside the store's transaction tx, so that they
// commit or roll back with it (libs/jobs' Client.InsertTx).
type JobInserter func(ctx context.Context, tx pgx.Tx, jobs []port.Job) error

// WithJobInserter makes [port.JobEnqueuer] insert jobs with f instead of
// writing them to the job_outbox table.
func WithJobInserter(f JobInserter) Option {
	return func(s *Store) { s.jobs = f }
}

// New returns a store on pool. The pool's connections should decode
// timestamptz in UTC ([ConfigurePool]); the store converts to UTC anyway.
func New(pool *pgxpool.Pool, opts ...Option) *Store {
	s := &Store{pool: pool}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Pool returns the store's connection pool.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// ConfigurePool makes the connections of cfg decode timestamptz values in
// UTC, as the domain stores every instant.
func ConfigurePool(cfg *pgxpool.Config) {
	prev := cfg.AfterConnect
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		conn.TypeMap().RegisterType(&pgtype.Type{
			Name:  "timestamptz",
			OID:   pgtype.TimestamptzOID,
			Codec: &pgtype.TimestamptzCodec{ScanLocation: time.UTC},
		})
		if prev != nil {
			return prev(ctx, conn)
		}
		return nil
	}
}

// Open parses dsn, opens a pool configured by [ConfigurePool] and returns a
// store on it.
func Open(ctx context.Context, dsn string, opts ...Option) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("store: parse dsn: %w", err)
	}
	ConfigurePool(cfg)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: open pool: %w", err)
	}
	return New(pool, opts...), nil
}

type txKey struct{}

// InTx implements [port.Transactor]: fn runs in a READ COMMITTED
// transaction committed when it returns nil and rolled back otherwise or on
// panic. A nested call joins the transaction carried by ctx.
func (s *Store) InTx(ctx context.Context, fn func(ctx context.Context, r port.Repos) error) (err error) {
	if t, ok := ctx.Value(txKey{}).(*txState); ok && t.store == s {
		return fn(ctx, t.repos)
	}
	pgxTx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	t := &txState{store: s, tx: pgxTx, rls: s.rls}
	t.repos = t.bind()
	done := false
	defer func() {
		if done {
			return
		}
		// fn panicked: roll back and re-raise.
		_ = pgxTx.Rollback(context.WithoutCancel(ctx))
	}()
	if err := fn(context.WithValue(ctx, txKey{}, t), t.repos); err != nil {
		done = true
		if rbErr := pgxTx.Rollback(context.WithoutCancel(ctx)); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			return errors.Join(err, fmt.Errorf("store: rollback: %w", rbErr))
		}
		return err
	}
	done = true
	if err := pgxTx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit: %w", mapErr(err))
	}
	return nil
}

// InPgxTx is [Store.InTx] that also hands fn the pgx transaction the
// repositories are bound to, for the stores outside this package that write
// in the same transaction (the PostgreSQL event store of the ingest path).
// Statements fn runs on tx bypass the repositories: they must not change
// tanvo.organization_id, and the row-level security scope the repositories
// set is not theirs to rely on. A nested call joins the transaction
// carried by ctx.
func (s *Store) InPgxTx(ctx context.Context, fn func(ctx context.Context, r port.Repos, tx pgx.Tx) error) error {
	return s.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		t, ok := ctx.Value(txKey{}).(*txState)
		if !ok || t.store != s {
			return errors.New("store: transaction not in context")
		}
		return fn(ctx, r, t.tx)
	})
}
