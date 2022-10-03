package main

import (
	"flag"
	"log"
)

var argv = struct {
	server
}{}

func main() {
	flag.StringVar(&argv.host, "host", "0.0.0.0", "Host interface to listen to")
	flag.StringVar(&argv.port, "port", "80", "Host port to listen to")
	flag.StringVar(&argv.tlsServerCertFile, "tls-server-cert", "", "A PEM encoded server certificate file")
	flag.StringVar(&argv.tlsServerKeyFile, "tls-server-key", "", "A PEM encoded server private key file")
	flag.StringVar(&argv.nntpServer, "nntp-server", "", "NNTP provider server")
	flag.StringVar(&argv.nntpUser, "nntp-user", "", "NNTP provider username")
	flag.StringVar(&argv.nntpPass, "nntp-pass", "", "NNTP provider password")
	flag.IntVar(&argv.nntpConcurrency, "nntp-concurrency", 50, "maximum concurrent NNTP connections")
	flag.StringVar(&argv.defaultNewsgroup, "default-newsgroup", "alt.binaries.misc", "Default Usenet Newsgroup to get or post articles")
	flag.Uint64Var(&argv.articleSizeLimit, "article-size-limit", 1024*1024*4, "Maximum article length to read")

	flag.Parse()

	if err := argv.Serve(); err != nil {
		log.Fatal(err)
	}
}
