// process entrypoint
package main

import (
	"log"
	"ridepulse/services/pricing-service/internal/app"
)
func main(){
	if err:=app.Run();err!=nil{
		log.Fatal(err)
	}
}