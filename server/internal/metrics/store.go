package metrics

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/niltavi/lerryn/server/internal/store"
)

// scrapeTimeout bounds the queries of one scrape, so a slow database
// cannot pile up scrapes.
const scrapeTimeout = 3 * time.Second

// RunnerOnlineWindow is how recently a runner must have been seen to count
// as online. Runners renew run leases every 2 seconds and poll for work
// continuously, so a runner silent for longer than a lease is gone.
const RunnerOnlineWindow = 30 * time.Second

// runStatuses are reported even at zero, so that a status that empties
// shows 0 rather than disappearing from the graph.
var runStatuses = []string{
	store.RunQueued, store.RunPreparing, store.RunRunning, store.RunCancelling,
	store.RunCompleted, store.RunFailed, store.RunTimedOut, store.RunCancelled,
	store.RunInterrupted, store.RunSkipped,
}

var activeStatuses = []string{store.RunPreparing, store.RunRunning, store.RunCancelling}

// RegisterStore adds the connection pool statistics and the run queue and
// runner figures read from db at scrape time. Query errors are logged and
// reported through lerryn_store_up; they do not fail the scrape.
func (m *Metrics) RegisterStore(db *store.DB, log *slog.Logger) {
	if m == nil {
		return
	}
	m.reg.MustRegister(
		collectors.NewDBStatsCollector(db.DB.DB, db.Native().Name()),
		&storeCollector{db: db, log: log},
	)
}

var (
	descStoreUp = prometheus.NewDesc(Namespace+"_store_up",
		"Whether the last scrape could read the run and runner figures from the database.", nil, nil)
	descRuns = prometheus.NewDesc(Namespace+"_runs",
		"Runs in the database by status.", []string{"status"}, nil)
	descQueueDepth = prometheus.NewDesc(Namespace+"_run_queue_depth",
		"Runs waiting for a runner.", nil, nil)
	descQueueAge = prometheus.NewDesc(Namespace+"_run_queue_oldest_seconds",
		"How long the oldest queued run has waited; 0 when the queue is empty.", nil, nil)
	descRunnersOnline = prometheus.NewDesc(Namespace+"_runners_online",
		"Runners seen within the online window.", nil, nil)
	descSlots = prometheus.NewDesc(Namespace+"_runner_slots",
		"Run slots of online runners.", nil, nil)
	descSlotsUsed = prometheus.NewDesc(Namespace+"_runner_slots_in_use",
		"Runs holding a runner slot: preparing, running or cancelling.", nil, nil)
)

type storeCollector struct {
	db  *store.DB
	log *slog.Logger
}

func (c *storeCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{descStoreUp, descRuns, descQueueDepth, descQueueAge, descRunnersOnline, descSlots, descSlotsUsed} {
		ch <- d
	}
}

func (c *storeCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), scrapeTimeout)
	defer cancel()
	s, err := readStoreStats(ctx, c.db)
	if err != nil {
		if c.log != nil {
			c.log.WarnContext(ctx, "read metrics from database", "error", err)
		}
		ch <- prometheus.MustNewConstMetric(descStoreUp, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(descStoreUp, prometheus.GaugeValue, 1)
	for _, st := range runStatuses {
		ch <- prometheus.MustNewConstMetric(descRuns, prometheus.GaugeValue, float64(s.byStatus[st]), st)
	}
	for st, n := range s.byStatus {
		if !slices.Contains(runStatuses, st) {
			ch <- prometheus.MustNewConstMetric(descRuns, prometheus.GaugeValue, float64(n), st)
		}
	}
	ch <- prometheus.MustNewConstMetric(descQueueDepth, prometheus.GaugeValue, float64(s.byStatus[store.RunQueued]))
	ch <- prometheus.MustNewConstMetric(descQueueAge, prometheus.GaugeValue, s.oldestQueued.Seconds())
	ch <- prometheus.MustNewConstMetric(descRunnersOnline, prometheus.GaugeValue, float64(s.runnersOnline))
	ch <- prometheus.MustNewConstMetric(descSlots, prometheus.GaugeValue, float64(s.slots))
	ch <- prometheus.MustNewConstMetric(descSlotsUsed, prometheus.GaugeValue, float64(s.slotsInUse))
}

type storeStats struct {
	byStatus      map[string]int64
	oldestQueued  time.Duration
	runnersOnline int64
	slots         int64
	slotsInUse    int64
}

func readStoreStats(ctx context.Context, db *store.DB) (storeStats, error) {
	s := storeStats{byStatus: map[string]int64{}}
	var counts []struct {
		Status string `bun:"status"`
		N      int64  `bun:"n"`
	}
	err := db.NewSelect().Model((*store.Run)(nil)).
		Column("status").ColumnExpr("count(*) AS n").
		Group("status").Scan(ctx, &counts)
	if err != nil {
		return s, err
	}
	for _, c := range counts {
		s.byStatus[c.Status] = c.N
	}

	var oldest store.Run
	err = db.NewSelect().Model(&oldest).Column("queued_at").
		Where("status = ?", store.RunQueued).
		Order("queued_at").Limit(1).Scan(ctx)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return s, err
	default:
		s.oldestQueued = max(0, time.Since(oldest.QueuedAt))
	}

	var online struct {
		N     int64 `bun:"n"`
		Slots int64 `bun:"slots"`
	}
	err = db.NewSelect().Model((*store.Runner)(nil)).
		ColumnExpr("count(*) AS n").ColumnExpr("coalesce(sum(slots), 0) AS slots").
		Where("last_seen_at > ?", store.Now().Add(-RunnerOnlineWindow)).
		Scan(ctx, &online)
	if err != nil {
		return s, err
	}
	s.runnersOnline, s.slots = online.N, online.Slots

	for _, st := range activeStatuses {
		s.slotsInUse += s.byStatus[st]
	}
	return s, nil
}
