//receives event from kafka 
//spawn n workers
//each worker will run pricing logic in parallel
package app
import(
	"context"
	"runtime"
	"ridepulse/services/pricing-service/internal/domain"
	// "ridepulse/services/pricing-service/internal/ports"
)

type WorkerPool struct{
	workers int;
	queue chan domain.RideRequestedEvent;
}

func NewWorkerPool(buffersize int)*WorkerPool{
	return &WorkerPool{
		workers:runtime.NumCPU(),//num of workers==num of cpu cores
		queue: make(chan domain.RideRequestedEvent,buffersize),

	}
}

func (p *WorkerPool) Start(
	ctx context.Context,
	handler func(event domain.RideRequestedEvent) error,
){
	//consumes events from channel and calls handler in parallel using workers
	//events consumed here are from Enqueue(), which is called by kafka consumer
	for i:=0;i<p.workers;i++{
		go func(workersID int){//<-----func foo (here anon but lets name it that)
			for {
				select{
				case event:=<-p.queue://worker consumes event from channel(queue) and calls handler
					_=handler(event)
				case <-ctx.Done():
					return
			}
		}}(i)
		//calls
		//func(0),func(1),func(2).... till num of workers
		//each func runs in seperate goroutine and consumes channel and calls handler

	}
}

func(p *WorkerPool) Enqueue(event domain.RideRequestedEvent){
	//this is  public API for pushing event to channel , workers will consume from this channel and process it
	//kafka calls this method whenever it receives new event 
	//this method blocks if buffer is full , kinda good for backpressure (auto slows kafka consumer when workers are busy)
	//kinda bad coz kafka gets blocked if workers are busy/slow
	//maybe ill profile this
	p.queue<-event//pushes event to channel(queue)
}