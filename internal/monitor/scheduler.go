package monitor

import (
	"fmt"
	"log"

	"github.com/robfig/cron/v3"
)

// Scheduler manages cron-scheduled periodic library scans.
type Scheduler struct {
	cronEngine *cron.Cron
	schedule   string
	onTrigger  func()
}

// NewScheduler creates a new cron scheduler.
func NewScheduler(schedule string, onTrigger func()) *Scheduler {
	return &Scheduler{
		cronEngine: cron.New(),
		schedule:   schedule,
		onTrigger:  onTrigger,
	}
}

// Start registers the job and begins cron scheduling.
func (s *Scheduler) Start() error {
	if s.schedule == "" {
		return nil
	}

	_, err := s.cronEngine.AddFunc(s.schedule, func() {
		log.Printf("[scheduler] triggered scheduled scan: %s", s.schedule)
		if s.onTrigger != nil {
			s.onTrigger()
		}
	})
	if err != nil {
		return fmt.Errorf("invalid cron schedule %q: %w", s.schedule, err)
	}

	s.cronEngine.Start()
	log.Printf("[scheduler] periodic scan registered with schedule %q", s.schedule)
	return nil
}

// Stop cleanly terminates the cron runner.
func (s *Scheduler) Stop() {
	if s.cronEngine != nil {
		s.cronEngine.Stop()
	}
}
