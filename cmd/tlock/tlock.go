package main

import (
	"flag"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/eshujiushiwo/tlock"
)

var httpAddr = flag.String("http_addr", "127.0.0.1:14000", "http listen address")

func main() {

	runtime.GOMAXPROCS(runtime.NumCPU())
	flag.Parse()

	a := tlock.NewApp()

	if len(*httpAddr) > 0 {
		a.StartHTTP(*httpAddr)
	}

	sc := make(chan os.Signal, 1)
	signal.Notify(sc,
		os.Kill,
		os.Interrupt,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGQUIT)

	<-sc

	defer a.Close()
}
