// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

// Command migrate-hash writes the atlas.sum of the analytics migrations,
// after a new migration file is added to store/migrations.
package main

import (
	"flag"
	"fmt"
	"os"

	"ariga.io/atlas/sql/migrate"
)

func main() {
	dir := flag.String("dir", "store/migrations", "the migrations directory")
	flag.Parse()
	if err := run(*dir); err != nil {
		fmt.Fprintln(os.Stderr, "migrate-hash:", err)
		os.Exit(1)
	}
}

func run(path string) error {
	dir, err := migrate.NewLocalDir(path)
	if err != nil {
		return err
	}
	sum, err := dir.Checksum()
	if err != nil {
		return err
	}
	return migrate.WriteSumFile(dir, sum)
}
