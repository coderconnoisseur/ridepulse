package main
import (
	"log"
	"ridepulse/services/matching-service/internal/app"
)
func main(){
	if err:=app.Run();err!=nil{
		log.Fatal(err)
	}
}