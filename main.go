package main

import (
	"log"
	"os"
	"path/filepath"

	"github.com/flynn/json5"
)

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

	if err := server.Serve(); err != nil {
		log.Fatal(err)
	}
}
