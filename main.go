package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/pprof"
	"syscall"

	"github.com/flynn/json5"
)

var cpuprofile = flag.String("cpuprofile", "", "write cpu profile to file")

func main() {
	var (
		err      error
		confPath string
		confData []byte
		server   server
	)

	if confPath, err = os.UserHomeDir(); err != nil {
		log.Fatal("cannot find user home dir")
	}
	confPath = filepath.Join(confPath, ".config", "usebin", "config.json")

	if confData, err = os.ReadFile(confPath); err != nil {
		log.Fatal("cannot read config file", err)
	}

	if err = json5.Unmarshal(confData, &server); err != nil {
		log.Fatal("cannot parse config file", err)
	}

	if server.CertFile != "" {
		server.CertFile = filepath.Join(confPath, server.CertFile)
	}
	if server.KeyFile != "" {
		server.KeyFile = filepath.Join(confPath, server.KeyFile)
	}

	flag.Parse()
	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			log.Fatal(err)
		}
		pprof.StartCPUProfile(f)
		log.Printf("CPU profiling started: %s", *cpuprofile)
		defer pprof.StopCPUProfile()
		c := make(chan os.Signal)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-c
			pprof.StopCPUProfile()
			log.Printf("CPU profiling saved at %s", *cpuprofile)
			os.Exit(1)
		}()
	}

	if err := server.Serve(); err != nil {
		log.Fatal(err)
	}
}
