package scheduler

import "time"

type Scheduler struct {
	interval time.Duration
	fn       func()
	quit     chan struct{}
}

func New(interval time.Duration, fn func()) *Scheduler {
	return &Scheduler{interval: interval, fn: fn, quit: make(chan struct{})}
}

func (s *Scheduler) Start() {
	go func() {
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.fn()
			case <-s.quit:
				return
			}
		}
	}()
}

func (s *Scheduler) Stop() {
	close(s.quit)
}
