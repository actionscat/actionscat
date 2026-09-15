package scheduler

import (
	"actionscat/internal/domain"
	"actionscat/internal/runner"
	"actionscat/internal/store"
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

var (
	ErrInvalidCron     = errors.New("invalid cron expression")
	ErrInvalidTimezone = errors.New("invalid timezone identifier")
)

type Config struct {
	TickInterval time.Duration
	BatchSize    int
}

type Scheduler struct {
	store        *store.SQLiteStore
	runner       *runner.Runner
	cronParser   cron.Parser
	tickInterval time.Duration
	batchSize    int

	mu     sync.Mutex
	wg     sync.WaitGroup
	cancel context.CancelFunc
}

func NewScheduler(store *store.SQLiteStore, runner *runner.Runner, cfg Config) *Scheduler {
	interval := cfg.TickInterval
	if interval <= 0 {
		interval = 1 * time.Second
	}
	batch := cfg.BatchSize
	if batch <= 0 {
		batch = 50
	}

	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

	return &Scheduler{
		store:        store,
		runner:       runner,
		cronParser:   parser,
		tickInterval: interval,
		batchSize:    batch,
	}
}

// CalculateNextRun evaluates the next run time according to the cron expression and timezone.
// It fails closed: invalid timezone names return ErrInvalidTimezone instead of silently defaulting to UTC.
func (s *Scheduler) CalculateNextRun(cronExpr string, timezone string, from time.Time) (time.Time, error) {
	schedule, err := s.cronParser.Parse(cronExpr)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %v", ErrInvalidCron, err)
	}

	loc := time.UTC
	if timezone != "" {
		l, err := time.LoadLocation(timezone)
		if err != nil {
			return time.Time{}, fmt.Errorf("%w: %q (%v)", ErrInvalidTimezone, timezone, err)
		}
		loc = l
	}

	next := schedule.Next(from.In(loc)).UTC()
	return next, nil
}

// Start initiates the persistent scheduling loop.
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	schedCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.mu.Unlock()

	s.wg.Add(1)
	go s.loop(schedCtx)
}

// Stop terminates the scheduling loop gracefully.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Scheduler) loop(ctx context.Context) {
	defer s.wg.Done()

	ticker := time.NewTicker(s.tickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Tick(ctx, time.Now().UTC())
		}
	}
}

// Tick evaluates all due schedules at the given point in time.
// It is strictly a non-blocking producer that creates Runs in database transactions
// and advances next_run_at without executing action code or waiting for run completion.
func (s *Scheduler) Tick(ctx context.Context, now time.Time) {
	dueSchedules, err := s.store.ListDueSchedules(ctx, now, s.batchSize)
	if err != nil {
		log.Printf("[scheduler] failed to list due schedules: %v", err)
		return
	}

	for _, sched := range dueSchedules {
		if ctx.Err() != nil {
			return
		}
		s.processDueSchedule(ctx, sched, now)
	}
}

func (s *Scheduler) processDueSchedule(ctx context.Context, sched *domain.Schedule, now time.Time) {
	// 1. Calculate next_run_at
	nextRunAt, err := s.CalculateNextRun(sched.CronExpr, sched.Timezone, now)
	if err != nil {
		log.Printf("[scheduler] invalid cron for schedule %s: %v", sched.ID, err)
		// Advance into future to prevent hot looping
		_ = s.store.AdvanceScheduleNextRun(ctx, sched.ID, now.Add(1*time.Hour))
		return
	}

	// 2. Prepare Run via Runner
	scheduledFor := sched.NextRunAt
	runReq := runner.CreateRunRequest{
		ActionID:    sched.ActionID,
		TriggerType: domain.TriggerTypeSchedule,
		TriggerMetadata: map[string]string{
			"schedule_id":   sched.ID,
			"cron_expr":     sched.CronExpr,
			"scheduled_for": scheduledFor.Format(time.RFC3339),
		},
	}

	run, err := s.runner.PrepareRun(ctx, runReq)
	if err != nil {
		// If the action is disabled or has no active build, log and advance next_run_at
		log.Printf("[scheduler] skipping schedule %s (action %s not runnable: %v)", sched.ID, sched.ActionID, err)
		_ = s.store.AdvanceScheduleNextRun(ctx, sched.ID, nextRunAt)
		return
	}

	// 3. Atomically record occurrence, create run, and advance schedule in one transaction
	err = s.store.RecordScheduleOccurrence(ctx, sched.ID, scheduledFor, nextRunAt, run)
	if err != nil {
		// Unique constraint violation on (schedule_id, scheduled_for) means duplicate run prevented!
		log.Printf("[scheduler] duplicate trigger prevented or error for schedule %s at %v: %v", sched.ID, scheduledFor, err)
		return
	}

	// 4. Trigger wakeup on the worker pool
	s.runner.TriggerWakeup()
}
