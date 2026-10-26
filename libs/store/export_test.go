package store

import (
	"context"

	"ariga.io/atlas/sql/migrate"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MigrateDir is [Migrate] on the migrations of dir instead of the embedded
// ones.
func MigrateDir(ctx context.Context, pool *pgxpool.Pool, dir migrate.Dir, opts ...MigrateOption) error {
	files, err := dir.Files()
	if err != nil {
		return err
	}
	sum, err := dir.Checksum()
	if err != nil {
		return err
	}
	return migrateFiles(ctx, pool, files, sum, opts...)
}
