package main

import (
	"log"
	"math/rand"
	"ridepulse/services/matching-service/internal/app"
	"time"
)
func main(){
	rand.Seed(time.Now().UnixNano())
	if err:=app.Run();err!=nil{
		log.Fatal(err)
	}
}