package main

import (
	// "fmt"
	"context"
	"os"
	"os/signal"
	"ridepulse/services/api-gateway/internal/app"
	"time"
	// "github.com/gin-gonic/gin"
	"net/http"

	// "golang.org/x/tools/go/analysis/passes/defers"
)
func main(){
	r:=app.NewApp()
	
	serv:=&http.Server{
		Addr:":8080",
		Handler:r,
		
	}
	go func(){//start server in a goroutine
		if err:=serv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			panic(err)
		}
	}()
	//i dont want server to shut down immediately
	//so i am adding a channel to listen for interrupt signal
	//it will complete graceful shutdown

	quit:=make(chan os.Signal,1)
	signal.Notify(quit,os.Interrupt)
	<-quit // signal arrived here

	ctx,cancel:= context.WithTimeout(context.Background(),5*time.Second)//graceful shutdown
	defer cancel()
	serv.Shutdown(ctx)

}