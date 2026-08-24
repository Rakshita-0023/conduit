package main

import (
	"context"
	"errors"
	"flag"
	"github.com/conduit-mcp/conduit/internal/app"
	"github.com/conduit-mcp/conduit/internal/config"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

var version = "dev"

func main() {
	path := flag.String("config", "conduit.yaml", "path to configuration")
	flag.Parse()
	c, e := config.Load(*path)
	if e != nil {
		log.Fatal(e)
	}
	a, e := app.Start(context.Background(), c, version)
	if e != nil {
		log.Fatal(e)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- a.Server.Serve(a.Listener) }()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	select {
	case e := <-serveErr:
		if !errors.Is(e, http.ErrServerClosed) && !errors.Is(e, net.ErrClosed) {
			_ = a.Close(context.Background())
			log.Fatal(e)
		}
		return
	case <-sig:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if e := a.Close(ctx); e != nil {
		log.Print(e)
	}
}
