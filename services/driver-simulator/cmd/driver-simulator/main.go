package main
import(
	"context"
	"log"
	"ridepulse/services/driver-simulator/internal/redis"
	"ridepulse/services/driver-simulator/internal/simulator"
)
func main(){
	ctx:=context.Background()
	r:=redis.New("localhost:6379")
	sim:=simulator.New(100)
	log.Println("starting driver simulator")
	sim.Run(ctx,r)
	select{}
}
