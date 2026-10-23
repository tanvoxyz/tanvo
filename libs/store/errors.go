package store

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// PostgreSQL error codes mapped to the errors of the repository contracts.
const (
	pgUniqueViolation     = "23505"
	pgExclusionViolation  = "23P01"
	pgForeignKeyViolation = "23503"
	pgCheckViolation      = "23514"
	pgNotNullViolation    = "23502"
	pgInvalidText         = "22P02"
	pgStringTooLong       = "22001"
	pgNumericOutOfRange   = "22003"
)

// mapErr translates database errors into the errors of the repository
// contracts, keeping the original error in the chain:
//
//   - a unique or exclusion violation is core.ErrConflict;
//   - a foreign key violation is core.ErrConflict when the row is still
//     referenced (a delete) and core.ErrNotFound when the referenced row is
//     missing (an insert or update);
//   - check, not-null and invalid-input violations are core.ErrInvalidValue;
//   - pgx.ErrNoRows is core.ErrNotFound.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %w", core.ErrNotFound, err)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.Code {
	case pgUniqueViolation, pgExclusionViolation:
		return fmt.Errorf("%s: %w: %w", pgErr.ConstraintName, core.ErrConflict, err)
	case pgForeignKeyViolation:
		if strings.Contains(pgErr.Detail, "is still referenced") {
			return fmt.Errorf("%s: still referenced: %w: %w", pgErr.ConstraintName, core.ErrConflict, err)
		}
		return fmt.Errorf("%s: referenced row missing: %w: %w", pgErr.ConstraintName, core.ErrNotFound, err)
	case pgCheckViolation, pgNotNullViolation, pgInvalidText, pgStringTooLong, pgNumericOutOfRange:
		return fmt.Errorf("%w: %w", core.ErrInvalidValue, err)
	default:
		return err
	}
}

// notFound returns core.ErrNotFound for entity key.
func notFound(entity string, key any) error {
	return fmt.Errorf("%s %v: %w", entity, key, core.ErrNotFound)
}

// conflict returns core.ErrConflict for entity and the rule it breaks.
func conflict(entity, rule string) error {
	return fmt.Errorf("%s: %s: %w", entity, rule, core.ErrConflict)
}

// getErr maps the error of a single-row read: no row is
// core.ErrNotFound naming entity and key.
func getErr(entity string, key any, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound(entity, key)
	}
	return fmt.Errorf("%s %v: %w", entity, key, mapErr(err))
}

// insertErr maps the result of an INSERT … ON CONFLICT DO NOTHING: no row
// written is core.ErrConflict.
func insertErr(entity string, n int64, err error) error {
	if err != nil {
		return fmt.Errorf("create %s: %w", entity, mapErr(err))
	}
	if n == 0 {
		return conflict(entity, "unique key taken")
	}
	return nil
}

// updateErr maps the result of an UPDATE or DELETE of one row: no row is
// core.ErrNotFound.
func updateErr(entity string, key any, n int64, err error) error {
	if err != nil {
		return fmt.Errorf("%s %v: %w", entity, key, mapErr(err))
	}
	if n == 0 {
		return notFound(entity, key)
	}
	return nil
}
