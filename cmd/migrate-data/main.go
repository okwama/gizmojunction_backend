// Command migrate-data copies row data from Supabase (source) into Neon
// (destination) — both are plain Postgres, so this is a generic two-connection
// data copy, not anything Supabase-specific. It exists because this
// environment has no pg_dump/psql on PATH, and because a hand-maintained
// table order rots as migrations add tables — instead, the copy order is
// derived by introspecting the destination's actual foreign-key graph and
// topologically sorting it (parents before children).
//
// Every insert is an upsert (ON CONFLICT DO UPDATE on the table's primary
// key), so this tool is safe to re-run: once for the initial full mirror,
// then again scoped to a handful of tables (--tables=orders,order_items)
// right before that domain's Phase 5 cutover to catch anything written to
// Supabase since the last run.
//
// Usage:
//
//	migrate-data                              # copy every table, dependency order
//	migrate-data --tables=orders,order_items  # copy only these tables
//	migrate-data --verify-only                # row-count diff only, no writes
//	migrate-data --source=... --dest=...      # override config.Load()'s URLs
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"gizmojunction/backend/internal/config"
)

// riverInternalTables are managed by river's own migrator (see cmd/migrate)
// and never existed in Supabase — always excluded from the copy.
var riverInternalTables = map[string]bool{
	"river_job": true, "river_queue": true, "river_leader": true,
	"river_migration": true, "river_client": true, "river_client_queue": true,
	"schema_migrations": true,
}

const batchSize = 500

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	sourceURL := cfg.SupabaseDatabaseURL
	destURL := cfg.DatabaseURL
	var tablesFilter []string
	verifyOnly := false
	fresh := false

	for _, arg := range os.Args[1:] {
		switch {
		case arg == "--verify-only":
			verifyOnly = true
		case arg == "--fresh":
			fresh = true
		case strings.HasPrefix(arg, "--tables="):
			tablesFilter = strings.Split(strings.TrimPrefix(arg, "--tables="), ",")
		case strings.HasPrefix(arg, "--source="):
			sourceURL = strings.TrimPrefix(arg, "--source=")
		case strings.HasPrefix(arg, "--dest="):
			destURL = strings.TrimPrefix(arg, "--dest=")
		default:
			log.Fatalf("unrecognized argument: %s", arg)
		}
	}

	if sourceURL == "" {
		log.Fatal("source database URL required: set SUPABASE_DATABASE_URL in backend/.env, or pass --source=")
	}

	ctx := context.Background()

	srcPool, err := pgxpool.New(ctx, sourceURL)
	if err != nil {
		log.Fatalf("connect source: %v", err)
	}
	defer srcPool.Close()

	destPool, err := pgxpool.New(ctx, destURL)
	if err != nil {
		log.Fatalf("connect dest: %v", err)
	}
	defer destPool.Close()

	tables, err := discoverTablesInOrder(ctx, destPool)
	if err != nil {
		log.Fatalf("discover tables: %v", err)
	}

	if err := restrictToCommonColumns(ctx, srcPool, tables); err != nil {
		log.Fatalf("reconcile source/dest columns: %v", err)
	}

	if len(tablesFilter) > 0 {
		wanted := make(map[string]bool)
		for _, t := range tablesFilter {
			wanted[strings.TrimSpace(t)] = true
		}
		filtered := tables[:0]
		for _, t := range tables {
			if wanted[t.Name] {
				filtered = append(filtered, t)
			}
		}
		tables = filtered
	}

	if len(tables) == 0 {
		log.Fatal("no tables to process (check --tables= against actual table names)")
	}

	fmt.Printf("processing %d table(s) in dependency order: %s\n\n", len(tables), tableNames(tables))

	if fresh && !verifyOnly {
		fmt.Println("--fresh: destination tables will be TRUNCATEd before copying — any existing rows in them (including test/seed data) will be permanently deleted.")
	}

	if verifyOnly {
		for _, t := range tables {
			if err := verifyTable(ctx, srcPool, destPool, t); err != nil {
				fmt.Printf("  %-30s VERIFY FAILED: %v\n", t.Name, err)
			}
		}
	} else {
		if err := copyAllWithRetry(ctx, srcPool, destPool, tables, fresh); err != nil {
			log.Fatal(err)
		}
	}

	if !verifyOnly {
		fmt.Println("\nverifying row counts...")
		for _, t := range tables {
			if err := verifyTable(ctx, srcPool, destPool, t); err != nil {
				fmt.Printf("  %-30s VERIFY FAILED: %v\n", t.Name, err)
			}
		}
	}
}

// copyAllWithRetry copies every table in order, but a table that fails on a
// foreign-key violation is deferred and retried in a later pass instead of
// aborting the whole run — self-healing against any table-ordering edge case
// (a real dependency cycle, or a subtlety in the FK introspection query)
// rather than requiring the static topological sort to be perfect. Passes
// repeat until either every deferred table succeeds or a full pass makes no
// progress at all, at which point the remaining errors are real and reported.
func copyAllWithRetry(ctx context.Context, src, dest *pgxpool.Pool, tables []table, fresh bool) error {
	pending := tables
	for attempt := 1; len(pending) > 0; attempt++ {
		if attempt > 1 {
			fmt.Printf("\nretry pass %d for %d table(s) that hit a foreign-key ordering issue: %s\n", attempt, len(pending), tableNames(pending))
		}

		var deferred []table
		var lastErr error
		for _, t := range pending {
			if err := copyTable(ctx, src, dest, t, fresh); err != nil {
				if isFKViolation(err) {
					deferred = append(deferred, t)
					lastErr = err
					continue
				}
				return fmt.Errorf("copy %s: %w", t.Name, err)
			}
		}

		if len(deferred) == len(pending) {
			return fmt.Errorf("no progress on retry pass %d, %d table(s) still failing on foreign-key ordering: %s (last error: %w)",
				attempt, len(deferred), tableNames(deferred), lastErr)
		}
		pending = deferred
	}
	return nil
}

func isFKViolation(err error) bool {
	return strings.Contains(err.Error(), "SQLSTATE 23503")
}

type table struct {
	Name       string
	PrimaryKey []string
	Columns    []string
	// SelfRefColumns are columns with a foreign key back to this same table
	// (e.g. categories.parent_id -> categories.id). Rows from the source
	// don't arrive in parent-before-child order, so these columns are
	// inserted as NULL on the first pass and back-filled in a second pass
	// once every row exists — handles any hierarchy depth generically.
	SelfRefColumns []string
}

func tableNames(tables []table) string {
	names := make([]string, len(tables))
	for i, t := range tables {
		names[i] = t.Name
	}
	return strings.Join(names, ", ")
}

// discoverTablesInOrder introspects the destination's public schema: every
// base table, its primary key columns, its full column list, and its
// foreign-key edges — then returns tables topologically sorted so parents
// always precede children (a table with no incoming ordering constraint from
// an unprocessed dependency is safe to copy next).
func discoverTablesInOrder(ctx context.Context, pool *pgxpool.Pool) ([]table, error) {
	rows, err := pool.Query(ctx, `
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_type = 'BASE TABLE'
		ORDER BY table_name
	`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	allNames, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("scan table names: %w", err)
	}

	byName := make(map[string]*table)
	var order []string
	for _, name := range allNames {
		if riverInternalTables[name] {
			continue
		}
		byName[name] = &table{Name: name}
		order = append(order, name)
	}

	// Primary keys.
	pkRows, err := pool.Query(ctx, `
		SELECT tc.table_name, kcu.column_name
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
			ON tc.constraint_name = kcu.constraint_name AND tc.table_schema = kcu.table_schema
		WHERE tc.constraint_type = 'PRIMARY KEY' AND tc.table_schema = 'public'
		ORDER BY tc.table_name, kcu.ordinal_position
	`)
	if err != nil {
		return nil, fmt.Errorf("list primary keys: %w", err)
	}
	for pkRows.Next() {
		var tableName, col string
		if err := pkRows.Scan(&tableName, &col); err != nil {
			return nil, err
		}
		if t, ok := byName[tableName]; ok {
			t.PrimaryKey = append(t.PrimaryKey, col)
		}
	}
	pkRows.Close()

	// Columns, in table order.
	colRows, err := pool.Query(ctx, `
		SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema = 'public'
		ORDER BY table_name, ordinal_position
	`)
	if err != nil {
		return nil, fmt.Errorf("list columns: %w", err)
	}
	for colRows.Next() {
		var tableName, col string
		if err := colRows.Scan(&tableName, &col); err != nil {
			return nil, err
		}
		if t, ok := byName[tableName]; ok {
			t.Columns = append(t.Columns, col)
		}
	}
	colRows.Close()

	// Foreign-key edges: child depends on parent. Also captures the
	// referencing column, needed to detect self-referencing FKs.
	fkRows, err := pool.Query(ctx, `
		SELECT tc.table_name AS child_table, kcu.column_name AS child_column, ccu.table_name AS parent_table
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
			ON tc.constraint_name = kcu.constraint_name AND tc.table_schema = kcu.table_schema
		JOIN information_schema.constraint_column_usage ccu
			ON tc.constraint_name = ccu.constraint_name AND tc.table_schema = ccu.table_schema
		WHERE tc.constraint_type = 'FOREIGN KEY' AND tc.table_schema = 'public'
	`)
	if err != nil {
		return nil, fmt.Errorf("list foreign keys: %w", err)
	}
	deps := make(map[string]map[string]bool) // child -> set of parents
	for fkRows.Next() {
		var child, childCol, parent string
		if err := fkRows.Scan(&child, &childCol, &parent); err != nil {
			return nil, err
		}
		if child == parent {
			// Self-referencing FK (e.g. categories.parent_id) doesn't order
			// against itself, but copyTable needs to know about it to avoid
			// inserting rows out of hierarchy order.
			if t, ok := byName[child]; ok {
				t.SelfRefColumns = append(t.SelfRefColumns, childCol)
			}
			continue
		}
		if _, ok := byName[child]; !ok {
			continue
		}
		if _, ok := byName[parent]; !ok {
			continue
		}
		if deps[child] == nil {
			deps[child] = make(map[string]bool)
		}
		deps[child][parent] = true
	}
	fkRows.Close()

	return topoSort(order, deps, byName)
}

// topoSort orders tables so every table appears after all tables it
// restrictToCommonColumns narrows each table's column list to the
// intersection of source and destination columns, mutating tables in place.
// Schema drift between Supabase (source) and Neon (destination) is real and
// expected mid-migration (e.g. products.rating/review_count exist in Neon
// from earlier Phase 1 work but were never backported to Supabase's live
// schema) — rather than failing the whole run on one drifted table, any
// column missing from either side is simply left out of the copy, so a
// dest-only column keeps its table default and a source-only column is
// just not copied. PrimaryKey/SelfRefColumns are re-filtered to match.
func restrictToCommonColumns(ctx context.Context, src *pgxpool.Pool, tables []table) error {
	rows, err := src.Query(ctx, `
		SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema = 'public'
	`)
	if err != nil {
		return fmt.Errorf("list source columns: %w", err)
	}
	sourceCols := make(map[string]map[string]bool)
	for rows.Next() {
		var tableName, col string
		if err := rows.Scan(&tableName, &col); err != nil {
			return err
		}
		if sourceCols[tableName] == nil {
			sourceCols[tableName] = make(map[string]bool)
		}
		sourceCols[tableName][col] = true
	}
	rows.Close()
	if rows.Err() != nil {
		return rows.Err()
	}

	for i := range tables {
		t := &tables[i]
		srcSet := sourceCols[t.Name]
		if srcSet == nil {
			continue // table missing entirely from source — copyTable already handles this via isMissingRelation
		}

		var kept []string
		var dropped []string
		for _, c := range t.Columns {
			if srcSet[c] {
				kept = append(kept, c)
			} else {
				dropped = append(dropped, c)
			}
		}
		if len(dropped) > 0 {
			fmt.Printf("  note: %s has column(s) not in source, skipping them: %s\n", t.Name, strings.Join(dropped, ", "))
			t.Columns = kept

			keptSet := make(map[string]bool, len(kept))
			for _, c := range kept {
				keptSet[c] = true
			}
			var pk []string
			for _, c := range t.PrimaryKey {
				if keptSet[c] {
					pk = append(pk, c)
				}
			}
			t.PrimaryKey = pk

			var selfRef []string
			for _, c := range t.SelfRefColumns {
				if keptSet[c] {
					selfRef = append(selfRef, c)
				}
			}
			t.SelfRefColumns = selfRef
		}
	}
	return nil
}

// topoSort orders tables so every table appears after all tables it
// depends on. Falls back to alphabetical order among tables with no
// remaining dependencies, for deterministic output.
func topoSort(names []string, deps map[string]map[string]bool, byName map[string]*table) ([]table, error) {
	remaining := make(map[string]bool)
	for _, n := range names {
		remaining[n] = true
	}

	var result []table
	for len(remaining) > 0 {
		progressed := false
		for _, n := range names {
			if !remaining[n] {
				continue
			}
			ready := true
			for parent := range deps[n] {
				if remaining[parent] {
					ready = false
					break
				}
			}
			if ready {
				result = append(result, *byName[n])
				delete(remaining, n)
				progressed = true
			}
		}
		if !progressed {
			// Circular dependency (shouldn't happen with real FKs) — append
			// whatever's left in name order rather than hang.
			for _, n := range names {
				if remaining[n] {
					result = append(result, *byName[n])
					delete(remaining, n)
				}
			}
		}
	}
	return result, nil
}

// copyTable streams rows from source and upserts them into dest in batches.
// Gracefully skips tables that don't exist in source (e.g. refresh_tokens,
// which is Go-backend-native and was never a Supabase table).
func copyTable(ctx context.Context, src, dest *pgxpool.Pool, t table, fresh bool) error {
	if len(t.PrimaryKey) == 0 {
		fmt.Printf("  %-30s SKIP (no primary key found — cannot upsert)\n", t.Name)
		return nil
	}

	quotedCols := quoteIdents(t.Columns)
	orderBy := strings.Join(quoteIdents(t.PrimaryKey), ", ")
	selectSQL := fmt.Sprintf(`SELECT %s FROM %s ORDER BY %s LIMIT $1 OFFSET $2`,
		strings.Join(quotedCols, ", "), pgx.Identifier{t.Name}.Sanitize(), orderBy)

	// Probe once with an empty page to confirm the table exists in source
	// before truncating dest — same "never wipe on a typo/missing table"
	// guarantee the old single-cursor version had.
	probe, err := src.Query(ctx, selectSQL, 0, 0)
	if err != nil {
		if isMissingRelation(err) {
			fmt.Printf("  %-30s SKIP (not present in source)\n", t.Name)
			return nil
		}
		return fmt.Errorf("query source: %w", err)
	}
	probe.Close()

	// Only truncate once we know the source table is real — never wipe a
	// destination table just because a --tables= typo or a Go/Neon-native
	// table (refresh_tokens, river_notification) has no source counterpart.
	// CASCADE is safe here because tables are processed in dependency order
	// (parents before children): any child cascaded away by truncating its
	// parent hasn't been copied into yet, so nothing already-migrated is lost.
	if fresh {
		if _, err := dest.Exec(ctx, fmt.Sprintf(`TRUNCATE TABLE %s CASCADE`, pgx.Identifier{t.Name}.Sanitize())); err != nil {
			return fmt.Errorf("truncate dest: %w", err)
		}
	}

	insertSQL := buildUpsertSQL(t)

	colIdx := make(map[string]int, len(t.Columns))
	for i, c := range t.Columns {
		colIdx[c] = i
	}
	var selfRefIdx []int
	for _, c := range t.SelfRefColumns {
		selfRefIdx = append(selfRefIdx, colIdx[c])
	}
	var pkIdx []int
	for _, c := range t.PrimaryKey {
		pkIdx = append(pkIdx, colIdx[c])
	}

	// deferredUpdates holds {pk values, original self-ref values} for rows
	// whose self-ref column(s) were nulled out on insert and need a
	// second-pass UPDATE once every row in the table exists.
	type deferredUpdate struct {
		pk      []any
		selfRef []any
	}
	var deferredUpdates []deferredUpdate

	total := 0
	batch := make([][]any, 0, batchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		tx, err := dest.Begin(ctx)
		if err != nil {
			return err
		}
		for _, vals := range batch {
			if _, err := tx.Exec(ctx, insertSQL, vals...); err != nil {
				_ = tx.Rollback(ctx)
				return fmt.Errorf("upsert row: %w", err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		total += len(batch)
		batch = batch[:0]
		return nil
	}

	// Page through source with separate, short-lived queries (LIMIT/OFFSET)
	// rather than one cursor held open for the whole table — a single
	// long-lived SELECT interleaved with many destination round trips is
	// exactly what got this connection reset by Supabase's transaction-mode
	// pooler on a 7000+ row table. Bounding each query's lifetime to one
	// page is more robust regardless of which pooling mode is in play.
	for offset := 0; ; offset += batchSize {
		pageRows, err := src.Query(ctx, selectSQL, batchSize, offset)
		if err != nil {
			return fmt.Errorf("query source page (offset %d): %w", offset, err)
		}

		pageCount := 0
		for pageRows.Next() {
			vals, err := pageRows.Values()
			if err != nil {
				pageRows.Close()
				return fmt.Errorf("scan row: %w", err)
			}

			if len(selfRefIdx) > 0 {
				selfRefVals := make([]any, len(selfRefIdx))
				hasNonNull := false
				for i, idx := range selfRefIdx {
					selfRefVals[i] = vals[idx]
					if vals[idx] != nil {
						hasNonNull = true
					}
					vals[idx] = nil // insert NULL first; back-filled below once every row exists
				}
				if hasNonNull {
					pkVals := make([]any, len(pkIdx))
					for i, idx := range pkIdx {
						pkVals[i] = vals[idx]
					}
					deferredUpdates = append(deferredUpdates, deferredUpdate{pk: pkVals, selfRef: selfRefVals})
				}
			}

			batch = append(batch, vals)
			pageCount++
		}
		pageErr := pageRows.Err()
		pageRows.Close()
		if pageErr != nil {
			return fmt.Errorf("read source page (offset %d): %w", offset, pageErr)
		}

		if err := flush(); err != nil {
			return err
		}

		if pageCount < batchSize {
			break // last page was short (or empty) — done
		}
	}

	if len(deferredUpdates) > 0 {
		updateSQL := buildSelfRefUpdateSQL(t)
		tx, err := dest.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin self-ref update: %w", err)
		}
		for _, du := range deferredUpdates {
			args := append(append([]any{}, du.selfRef...), du.pk...)
			if _, err := tx.Exec(ctx, updateSQL, args...); err != nil {
				_ = tx.Rollback(ctx)
				return fmt.Errorf("back-fill self-ref columns: %w", err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit self-ref update: %w", err)
		}
		fmt.Printf("  %-30s %d row(s) copied (%d self-referencing column(s) back-filled)\n", t.Name, total, len(deferredUpdates))
		return nil
	}

	fmt.Printf("  %-30s %d row(s) copied\n", t.Name, total)
	return nil
}

// buildSelfRefUpdateSQL builds `UPDATE table SET col = $1, ... WHERE pk = $N
// AND ...` to back-fill self-referencing columns after every row exists.
func buildSelfRefUpdateSQL(t table) string {
	var sets []string
	i := 1
	for _, c := range t.SelfRefColumns {
		sets = append(sets, fmt.Sprintf("%s = $%d", pgx.Identifier{c}.Sanitize(), i))
		i++
	}
	var wheres []string
	for _, c := range t.PrimaryKey {
		wheres = append(wheres, fmt.Sprintf("%s = $%d", pgx.Identifier{c}.Sanitize(), i))
		i++
	}
	return fmt.Sprintf(`UPDATE %s SET %s WHERE %s`,
		pgx.Identifier{t.Name}.Sanitize(), strings.Join(sets, ", "), strings.Join(wheres, " AND "))
}

func buildUpsertSQL(t table) string {
	quotedCols := quoteIdents(t.Columns)
	placeholders := make([]string, len(t.Columns))
	for i := range t.Columns {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}

	pkSet := make(map[string]bool, len(t.PrimaryKey))
	for _, pk := range t.PrimaryKey {
		pkSet[pk] = true
	}
	var updates []string
	for _, col := range t.Columns {
		if pkSet[col] {
			continue
		}
		q := pgx.Identifier{col}.Sanitize()
		updates = append(updates, fmt.Sprintf("%s = EXCLUDED.%s", q, q))
	}

	conflictTarget := strings.Join(quoteIdents(t.PrimaryKey), ", ")
	updateClause := "DO NOTHING"
	if len(updates) > 0 {
		updateClause = "DO UPDATE SET " + strings.Join(updates, ", ")
	}

	return fmt.Sprintf(
		`INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (%s) %s`,
		pgx.Identifier{t.Name}.Sanitize(),
		strings.Join(quotedCols, ", "),
		strings.Join(placeholders, ", "),
		conflictTarget,
		updateClause,
	)
}

func verifyTable(ctx context.Context, src, dest *pgxpool.Pool, t table) error {
	var srcCount, destCount int64

	err := src.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s`, pgx.Identifier{t.Name}.Sanitize())).Scan(&srcCount)
	if err != nil {
		if isMissingRelation(err) {
			fmt.Printf("  %-30s (not present in source, skipped)\n", t.Name)
			return nil
		}
		return fmt.Errorf("count source: %w", err)
	}

	if err := dest.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s`, pgx.Identifier{t.Name}.Sanitize())).Scan(&destCount); err != nil {
		return fmt.Errorf("count dest: %w", err)
	}

	status := "OK"
	if srcCount != destCount {
		status = "MISMATCH"
	}
	fmt.Printf("  %-30s source=%-8d dest=%-8d %s\n", t.Name, srcCount, destCount, status)
	return nil
}

func quoteIdents(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = pgx.Identifier{n}.Sanitize()
	}
	return out
}

func isMissingRelation(err error) bool {
	return strings.Contains(err.Error(), "does not exist") && strings.Contains(err.Error(), "relation")
}
