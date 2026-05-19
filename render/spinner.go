package render

import (
	"fmt"
	"os"
	"sync"
	"time"
)

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const (
	ansiSpinner = "\x1b[38;5;39m"
	ansiDim     = "\x1b[38;5;241m"
	ansiClear   = "\x1b[0m"
)

type Spinner struct {
	msg   string
	start time.Time
	stop  chan struct{}
	done  sync.WaitGroup
}

func NewSpinner(msg string) *Spinner {
	s := &Spinner{
		msg:   msg,
		start: time.Now(),
		stop:  make(chan struct{}),
	}
	s.done.Add(1)
	go s.run()
	return s
}

func (s *Spinner) run() {
	defer s.done.Done()
	tick := time.NewTicker(80 * time.Millisecond)
	defer tick.Stop()

	i := 0
	for {
		select {
		case <-s.stop:
			fmt.Fprintf(os.Stderr, "\r\x1b[K")
			return
		case <-tick.C:
			elapsed := time.Since(s.start).Truncate(100 * time.Millisecond)
			frame := spinFrames[i%len(spinFrames)]
			fmt.Fprintf(os.Stderr, "\r\x1b[K%s%s%s %s %s%s%s",
				ansiSpinner, frame, ansiClear,
				s.msg,
				ansiDim, elapsed, ansiClear)
			i++
		}
	}
}

func (s *Spinner) Stop() time.Duration {
	close(s.stop)
	s.done.Wait()
	return time.Since(s.start)
}
