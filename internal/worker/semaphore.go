package worker

type Semaphore struct {
	ch chan struct{}
}

func NewSemaphore(max int) *Semaphore {
	if max <= 0 {
		max = 1
	}
	return &Semaphore{ch: make(chan struct{}, max)}
}

func (s *Semaphore) Acquire() { s.ch <- struct{}{} }
func (s *Semaphore) Release() { <-s.ch }

