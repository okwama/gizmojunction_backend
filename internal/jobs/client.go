package jobs

import (
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

type Deps struct {
	Pool       *pgxpool.Pool
	Email      *EmailSender
	SiteURL    string
	AdminEmail string
}

// NewClient builds the river client with every worker registered and the
// three periodic (cron-equivalent) jobs scheduled. The API server and the
// job runner share one process/binary per the migration plan's repo
// layout — no separate worker deployment for now.
//
// registerExtra lets callers (main.go) add workers defined in packages that
// can't be imported here without an import cycle (e.g. taxetims, which
// imports jobs for EmailSender) — main.go calls river.AddWorker on the
// same *river.Workers registry before the client is constructed.
func NewClient(deps Deps, registerExtra ...func(*river.Workers)) (*river.Client[pgx.Tx], error) {
	workers := river.NewWorkers()
	river.AddWorker(workers, &OrderNotificationWorker{Pool: deps.Pool, Email: deps.Email, SiteURL: deps.SiteURL, AdminEmail: deps.AdminEmail})
	river.AddWorker(workers, &OrderShippedNotificationWorker{Pool: deps.Pool, Email: deps.Email, SiteURL: deps.SiteURL})
	river.AddWorker(workers, &StockAlertsWorker{Pool: deps.Pool})
	river.AddWorker(workers, &DailySalesSnapshotWorker{Pool: deps.Pool})
	river.AddWorker(workers, &AbandonedCartRecoveryWorker{Pool: deps.Pool})
	for _, register := range registerExtra {
		register(workers)
	}

	return river.NewClient(riverpgxv5.New(deps.Pool), &river.Config{
		Queues: map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: 5},
		},
		Workers: workers,
		// RunOnStart is false throughout to match the originals, which only
		// ever ran on their cron schedule, never on deploy.
		PeriodicJobs: []*river.PeriodicJob{
			river.NewPeriodicJob(
				river.PeriodicInterval(1*time.Hour),
				func() (river.JobArgs, *river.InsertOpts) { return StockAlertsArgs{}, nil },
				&river.PeriodicJobOpts{RunOnStart: false},
			),
			river.NewPeriodicJob(
				river.PeriodicInterval(24*time.Hour),
				func() (river.JobArgs, *river.InsertOpts) { return DailySalesSnapshotArgs{}, nil },
				&river.PeriodicJobOpts{RunOnStart: false},
			),
			river.NewPeriodicJob(
				river.PeriodicInterval(30*time.Minute),
				func() (river.JobArgs, *river.InsertOpts) { return AbandonedCartRecoveryArgs{}, nil },
				&river.PeriodicJobOpts{RunOnStart: false},
			),
		},
	})
}
