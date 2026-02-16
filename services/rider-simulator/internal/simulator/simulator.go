package simulator
import(
	"bytes"
	"context"
	"encoding/json"
	// "fmt"
	"log"
	"net/http"
	"sync/atomic"
	"time"
)
type Simulator struct{
	client *http.Client
	url string
}

func New(url string)*Simulator{
	return &Simulator{
		client:&http.Client{
			Timeout:5*time.Second,
		},
		url:url,
	}
}
type RideRequest struct{
Pickup struct{
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}`json:"pickup"`
Drop struct{
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}`json:"drop"`
}

var requests int64;
var errors int64;
var totalLatency int64;

func (s *Simulator)sendRide (ctx context.Context){
	req:=RideRequest{}
	req.Pickup.Lat=12.9
	req.Pickup.Lng=77.6
	req.Drop.Lat=12.93
	req.Drop.Lng=77.65
	body,_:=json.Marshal(req)
	start:=time.Now()
	resp,err:=s.client.Post(
		s.url,
		"application/json",
		bytes.NewBuffer(body),
	)
	latency:=time.Since(start)
	atomic.AddInt64(&requests,1)
	atomic.AddInt64(&totalLatency,latency.Microseconds())
	if err!=nil{
		atomic.AddInt64(&errors,1)
	}
	if resp!=nil{
		resp.Body.Close()
	}

}

func (s *Simulator)Run(ctx context.Context,workers int,ratePerWorker time.Duration){
go func(){
	ticker:=time.NewTicker(1*time.Second)
	for range ticker.C{
		r:=atomic.SwapInt64(&requests,0)
		e:=atomic.SwapInt64(&errors,0)
		l:=atomic.SwapInt64(&totalLatency,0)
		var avg int64;
		if r>0{
			avg=l/r
		}
		log.Printf("RPS: %d | Errors: %d | Avg Latency(us): %d", r, e, avg)
	}
}()
for i :=0;i<workers;i++{
	go func(){
		ticker:=time.NewTicker(ratePerWorker)
		for {
			select{
				case<-ticker.C:
					s.sendRide(ctx)
				case<-ctx.Done():
					return
			}
		}
}()
	}
}