package app

import (
	"context"
	"runtime"

	"ridepulse/services/matching-service/internal/domain"
)

type WorkerPool struct {
	workers int
	queue   chan domain.RidePricedEvent
}

func NewWorkerPool(bufferSize int) *WorkerPool {
	return &WorkerPool{
		workers: runtime.NumCPU(),
		queue:   make(chan domain.RidePricedEvent, bufferSize),
	}
}

func (p *WorkerPool) Start(
	ctx context.Context,
	handler func(domain.RidePricedEvent) error,
) {
	for i := 0; i < p.workers; i++ {
		go func() {
			for {
				select {
				case event := <-p.queue:
					_ = handler(event)
				case <-ctx.Done():
					return
				}
			}
		}()
	}
}

func (p *WorkerPool) Submit(event domain.RidePricedEvent) {
	p.queue <- event
}
