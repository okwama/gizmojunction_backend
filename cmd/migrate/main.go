// Command migrate replays the .up.sql files under backend/migrations against
// DATABASE_URL, in filename order, tracking applied versions in
// public.schema_migrations. It intentionally avoids a third-party migration
// framework — the migrations are forward-only SQL files, and a full library
// isn't worth the dependency weight for that.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"gizmojunction/backend/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	migrationsDir := "migrations"
	var redoVersion int64 = -1
	status := false
	seedFile := ""
	for _, arg := range os.Args[1:] {
		if arg == "--status" {
			status = true
			continue
		}
		if v, ok := strings.CutPrefix(arg, "--redo="); ok {
			parsed, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				log.Fatalf("--redo: invalid version %q: %v", v, err)
			}
			redoVersion = parsed
			continue
		}
		if v, ok := strings.CutPrefix(arg, "--seed="); ok {
			seedFile = v
			continue
		}
		migrationsDir = arg
	}

	ctx := context.Background()

	connCfg, err := pgx.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("parse DATABASE_URL: %v", err)
	}
	connCfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	conn, err := pgx.ConnectConfig(ctx, connCfg)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS public.schema_migrations (
			version     bigint PRIMARY KEY,
			name        text NOT NULL,
			applied_at  timestamptz NOT NULL DEFAULT now()
		)
	`); err != nil {
		log.Fatalf("ensure schema_migrations: %v", err)
	}

	if redoVersion >= 0 {
		if _, err := conn.Exec(ctx, `DELETE FROM public.schema_migrations WHERE version = $1`, redoVersion); err != nil {
			log.Fatalf("--redo=%d: %v", redoVersion, err)
		}
		fmt.Printf("cleared recorded migration %d, it will be reapplied\n", redoVersion)
	}

	applied := map[int64]bool{}
	rows, err := conn.Query(ctx, `SELECT version FROM public.schema_migrations`)
	if err != nil {
		log.Fatalf("read schema_migrations: %v", err)
	}
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			log.Fatalf("scan version: %v", err)
		}
		applied[v] = true
	}
	rows.Close()

	if seedFile != "" {
		sqlBytes, err := os.ReadFile(seedFile)
		if err != nil {
			log.Fatalf("read seed file %s: %v", seedFile, err)
		}
		if _, err := conn.Exec(ctx, `SET search_path TO public`); err != nil {
			log.Fatalf("reset search_path: %v", err)
		}
		if _, err := conn.Exec(ctx, string(sqlBytes)); err != nil {
			log.Fatalf("seed %s: %v", seedFile, err)
		}
		fmt.Printf("seeded from %s\n", seedFile)
		return
	}

	if status {
		tableRows, err := conn.Query(ctx, `
			SELECT table_name FROM information_schema.tables
			WHERE table_schema = 'public' AND table_type = 'BASE TABLE'
			ORDER BY table_name
		`)
		if err != nil {
			log.Fatalf("list tables: %v", err)
		}
		count := 0
		for tableRows.Next() {
			var name string
			if err := tableRows.Scan(&name); err != nil {
				log.Fatalf("scan table name: %v", err)
			}
			fmt.Println(" -", name)
			count++
		}
		tableRows.Close()
		fmt.Printf("%d table(s) in public schema, %d migration(s) recorded\n", count, len(applied))
		return
	}

	files, err := filepath.Glob(filepath.Join(migrationsDir, "*.up.sql"))
	if err != nil {
		log.Fatalf("glob migrations: %v", err)
	}
	sort.Strings(files)

	appliedCount := 0
	for _, file := range files {
		base := filepath.Base(file)
		versionStr := strings.SplitN(base, "_", 2)[0]
		version, err := strconv.ParseInt(versionStr, 10, 64)
		if err != nil {
			log.Fatalf("%s: filename does not start with a numeric version: %v", base, err)
		}

		if applied[version] {
			continue
		}

		sqlBytes, err := os.ReadFile(file)
		if err != nil {
			log.Fatalf("read %s: %v", base, err)
		}

		fmt.Printf("applying %s...\n", base)

		tx, err := conn.Begin(ctx)
		if err != nil {
			log.Fatalf("%s: begin tx: %v", base, err)
		}

		// A prior migration file (the pg_dump baseline) may have left
		// search_path in a non-default state for this session; each
		// migration should run against a known-good public search_path
		// regardless of what earlier files did.
		if _, err := tx.Exec(ctx, `SET search_path TO public`); err != nil {
			_ = tx.Rollback(ctx)
			log.Fatalf("%s: reset search_path: %v", base, err)
		}

		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback(ctx)
			log.Fatalf("%s: %v", base, err)
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO public.schema_migrations (version, name) VALUES ($1, $2)`,
			version, base,
		); err != nil {
			_ = tx.Rollback(ctx)
			log.Fatalf("%s: record migration: %v", base, err)
		}

		if err := tx.Commit(ctx); err != nil {
			log.Fatalf("%s: commit: %v", base, err)
		}

		appliedCount++
	}

	fmt.Printf("done: %d migration(s) applied, %d already up to date\n", appliedCount, len(files)-appliedCount)

	// river's own job-queue tables (river_job, river_queue, ...) are
	// managed by its own migrator, tracked separately from
	// schema_migrations above.
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("river migration: connect: %v", err)
	}
	defer pool.Close()

	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		log.Fatalf("river migration: init: %v", err)
	}
	result, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	if err != nil {
		log.Fatalf("river migration: %v", err)
	}
	fmt.Printf("river: %d migration(s) applied\n", len(result.Versions))
}
