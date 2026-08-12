package main

import (
	"log"

	"ridepulse/services/assignment-service/internal/app"
)

func main() {
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
