package main

import (
	"context"
	"crypto/tls"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/textproto"
	"strings"

	"github.com/silenceper/pool"
	"gopkg.in/nntp.v0"
	"gopkg.in/pwgen.v0"
)

type server struct {
	host              string
	port              string
	nntpServer        string
	nntpUser          string
	nntpPass          string
	nntpConcurrency   int
	tlsServerCertFile string
	tlsServerKeyFile  string
	pool              pool.Pool
	defaultNewsgroup  string
}

//go:embed static
var staticFS embed.FS

func (s *server) handleMessage(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Path[3:]
	// https://developers.cloudflare.com/cache/about/default-cache-behavior/#default-cached-file-extensions
	if !strings.HasSuffix(name, ".csv") && !strings.HasSuffix(name, ".nfo") {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	messageID := nntp.MessageID(name[:len(name)-4])
	if messageID.Validate() != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.handleMessageGET(w, r, messageID)
	case http.MethodHead:
		s.handleMessageHEAD(w, r, messageID)
	case http.MethodPost:
		s.handleMessagePOST(w, r, messageID)
	}
}

func (s *server) handleMessageGET(w http.ResponseWriter, r *http.Request, messageID nntp.MessageID) {
	var (
		err     error
		conn    *nntp.Conn
		article *nntp.Article
	)
	if v, err := s.pool.Get(); err != nil {
		log.Printf("[ERROR] GET %s pool error: %s", messageID, err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		return
	} else {
		conn = v.(*nntp.Conn)
	}
	defer func() {
		if err == nil && conn != nil {
			s.pool.Put(conn)
		} else {
			s.pool.Close(conn)
		}
	}()
	if article, err = conn.CmdArticle(nntp.ArticleMessageID(messageID)); err != nil {
		log.Printf("[ERROR] GET %s NNTP error: %s", messageID, err.Error())
		w.WriteHeader(http.StatusNotFound)
		return
	}
	for key, values := range article.Header {
		for _, value := range values {
			w.Header().Add("X-Usenet-"+key, value)
		}
	}
	// text/plain makes browser render it instead of download
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err = io.Copy(w, article.Body); err != nil {
		log.Printf("[ERROR] GET %s write error: %s", messageID, err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	log.Printf("[INFO] GET %s", messageID)
}

func (s *server) handleMessageHEAD(w http.ResponseWriter, r *http.Request, messageID nntp.MessageID) {
	var (
		err  error
		conn *nntp.Conn
	)
	if v, err := s.pool.Get(); err != nil {
		log.Printf("[ERROR] HEAD %s pool error: %s", messageID, err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		return
	} else {
		conn = v.(*nntp.Conn)
	}
	defer func() {
		if err == nil && conn != nil {
			s.pool.Put(conn)
		} else {
			s.pool.Close(conn)
		}
	}()
	if _, err = conn.CmdStat(nntp.ArticleMessageID(messageID)); err != nil {
		log.Printf("[ERROR] HEAD %s NNTP error: %s", messageID, err.Error())
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	log.Printf("[INFO] HEAD %s", messageID)
}

func (s *server) handleMessagePOST(w http.ResponseWriter, r *http.Request, messageID nntp.MessageID) {
	var (
		err  error
		conn *nntp.Conn
		ngID string
	)
	if v, err := s.pool.Get(); err != nil {
		log.Printf("[ERROR] POST %s pool error: %s", messageID, err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		return
	} else {
		conn = v.(*nntp.Conn)
	}
	defer func() {
		if err == nil && conn != nil {
			s.pool.Put(conn)
		} else {
			s.pool.Close(conn)
		}
	}()
	query := r.URL.Query()
	header := make(textproto.MIMEHeader)
	for key, values := range r.Header {
		if strings.HasPrefix(key, "X-Usenet-") && len(key) > 9 {
			for _, value := range values {
				header.Add(key[9:], value)
			}
		}
	}
	if header.Get("From") == "" {
		if query.Get("f") != "" {
			header.Set("From", query.Get("f"))
		} else {
			// https://github.com/mbruel/ngPost/blob/7f4762b66ceefb5016a9fa6cefd310e0d3da6936/postFiles.sh#L121
			if ngID, err = pwgen.New(pwgen.RequireCapitalize,
				pwgen.NoAmbiguous, pwgen.RequireNumerals, pwgen.AllRandom); err != nil {
				log.Printf("[ERROR] POST %s pwgen error: %s", messageID, err.Error())
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			header.Set("From", ngID+"@ngPost.com")
		}
	}
	if header.Get("Newsgroups") == "" {
		if query.Get("g") != "" {
			header.Set("Newsgroups", query.Get("g"))
		} else {
			header.Set("Newsgroups", s.defaultNewsgroup)
		}
	}
	if header.Get("Subject") == "" {
		if query.Get("s") != "" {
			header.Set("Subject", query.Get("s"))
		} else {
			shortID := string(messageID.Short())
			parts := strings.SplitN(shortID, "@", 2)
			if len(parts) > 0 {
				header.Set("Subject", parts[0])
			}
			header.Set("Subject", shortID)
		}
	}
	if r.Header.Get("Content-Length") != "" {
		header.Set("Content-Length", r.Header.Get("Content-Length"))
	}
	article := &nntp.Article{
		MessageID: messageID,
		Header:    header,
		Body:      r.Body,
	}
	if err = conn.CmdPost(article); err != nil {
		log.Printf("[ERROR] POST %s NNTP error: %s", messageID, err.Error())
		w.WriteHeader(http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusOK)
	log.Printf("[INFO] POST %s", messageID)
}

func (s *server) Serve() (err error) {
	if s.nntpServer == "" {
		err = fmt.Errorf("--nntp-server is required")
		return
	}

	s.pool, err = pool.NewChannelPool(&pool.Config{
		InitialCap: 1,
		MaxCap:     s.nntpConcurrency,
		MaxIdle:    s.nntpConcurrency,
		Factory: func() (v interface{}, err error) {
			var d nntp.Dialer
			conn, err := d.Dial(context.Background(), "tcp", s.nntpServer)
			if err != nil {
				return
			}
			if s.nntpUser != "" {
				if err = conn.CmdAuthinfo(s.nntpUser, s.nntpPass); err != nil {
					conn.Close()
					return
				}
			}
			v = conn
			return
		},
		Close: func(v interface{}) (err error) {
			conn, ok := v.(*nntp.Conn)
			if !ok {
				err = fmt.Errorf("invalid conn type")
				return
			}
			return conn.Close()
		},
	})
	if err != nil {
		return
	}

	subFS, err := fs.Sub(staticFS, "static")
	if err != nil {
		return
	}
	httpFS := http.FS(subFS)
	fileServer := http.FileServer(httpFS)
	serveIndex := serveFileContents("index.html", httpFS)

	mux := http.NewServeMux()
	mux.HandleFunc("/m/", s.handleMessage)
	mux.Handle("/", intercept404(fileServer, serveIndex))

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// general headers
		w.Header().Set("Cache-Control", "public, max-age=2592000")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})

	httpServer := &http.Server{
		Addr:    s.host + ":" + s.port,
		Handler: handler,
	}

	if s.tlsServerCertFile != "" && s.tlsServerKeyFile != "" {
		var (
			serverCert tls.Certificate
		)

		serverCert, err = tls.LoadX509KeyPair(s.tlsServerCertFile, s.tlsServerKeyFile)
		if err != nil {
			return
		}

		httpServer.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{serverCert},
		}
		httpServer.TLSNextProto = make(map[string]func(*http.Server, *tls.Conn, http.Handler))

		log.Printf("Listening at https://%s\n", httpServer.Addr)
		err = httpServer.ListenAndServeTLS("", "")
	} else {
		log.Printf("Listening at http://%s\n", httpServer.Addr)
		err = httpServer.ListenAndServe()
	}
	return
}

type hookedResponseWriter struct {
	http.ResponseWriter
	got404 bool
}

func (hrw *hookedResponseWriter) WriteHeader(status int) {
	if status == http.StatusNotFound {
		// Don't actually write the 404 header, just set a flag.
		hrw.got404 = true
	} else {
		hrw.ResponseWriter.WriteHeader(status)
	}
}

func (hrw *hookedResponseWriter) Write(p []byte) (int, error) {
	if hrw.got404 {
		// No-op, but pretend that we wrote len(p) bytes to the writer.
		return len(p), nil
	}

	return hrw.ResponseWriter.Write(p)
}

func intercept404(handler, on404 http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hookedWriter := &hookedResponseWriter{ResponseWriter: w}
		handler.ServeHTTP(hookedWriter, r)

		if hookedWriter.got404 {
			on404.ServeHTTP(w, r)
		}
	})
}

func serveFileContents(file string, files http.FileSystem) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Restrict only to instances where the browser is looking for an HTML file
		if !strings.Contains(r.Header.Get("Accept"), "text/html") {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, "404 not found")

			return
		}

		// Open the file and return its contents using http.ServeContent
		index, err := files.Open(file)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, "%s not found", file)

			return
		}

		fi, err := index.Stat()
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, "%s not found", file)

			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeContent(w, r, fi.Name(), fi.ModTime(), index)
	}
}
