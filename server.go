package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"sync"

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
	articleSizeLimit  uint64
	bufPool           sync.Pool
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
	case http.MethodGet, http.MethodHead:
		s.handleMessageGET(w, r, messageID)
	case http.MethodPost:
		s.handleMessagePOST(w, r, messageID)
	}
}

func (s *server) handleMessageGET(w http.ResponseWriter, r *http.Request, messageID nntp.MessageID) {
	var (
		err         error
		conn        *nntp.Conn
		article     *nntp.Article
		v           any
		buf         []byte
		n           int
		size        int64
		ranges      []httpRange
		sendContent io.Reader
		sendSize    int64
		rangeReq    string
		done        bool
		code        int
	)

	ctype := "text/plain; charset=utf-8"

	if done, rangeReq = checkPreconditions(w, r); done {
		return
	}

	if v, err = s.pool.Get(); err != nil {
		log.Printf("[ERROR] %s %s pool error: %s", r.Method, messageID, err.Error())
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
		log.Printf("[ERROR] %s %s NNTP error: %s", r.Method, messageID, err.Error())
		w.WriteHeader(http.StatusNotFound)
		return
	}

	v = s.bufPool.Get()
	defer s.bufPool.Put(v)

	buf = v.([]byte)
	if n, err = io.ReadFull(article.Body, buf); err == io.ErrUnexpectedEOF {
		err = nil
	} else if err != nil {
		log.Printf("[ERROR] %s %s read error: %s", r.Method, messageID, err.Error())
		return
	}
	if _, err = article.Body.Read(nil); !errors.Is(err, io.EOF) {
		log.Printf("[ERROR] %s %s size exceeds limit", r.Method, messageID)
		w.WriteHeader(http.StatusInsufficientStorage)
		return
	}
	code = http.StatusOK
	size = int64(n)
	sendSize = size
	sendContent = bytes.NewReader(buf[:n])
	if size > 0 {
		if ranges, err = parseRange(rangeReq, size); err != nil {
			if err == errNoOverlap {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
			}
			log.Printf("[ERROR] %s %s invalid range", r.Method, messageID)
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if sumRangesSize(ranges) > size {
			// The total number of bytes in all the ranges
			// is larger than the size of the file by
			// itself, so this is probably an attack, or a
			// dumb client. Ignore the range request.
			ranges = nil
		}
	}

	if len(ranges) == 1 {
		// RFC 7233, Section 4.1:
		// "If a single part is being transferred, the server
		// generating the 206 response MUST generate a
		// Content-Range header field, describing what range
		// of the selected representation is enclosed, and a
		// payload consisting of the range.
		// ...
		// A server MUST NOT generate a multipart response to
		// a request for a single range, since a client that
		// does not request multiple parts might not support
		// multipart responses."
		ra := ranges[0]
		sendContent = bytes.NewReader(buf[ra.start : ra.start+ra.length])
		sendSize = ra.length
		code = http.StatusPartialContent
		w.Header().Set("Content-Range", ra.contentRange(size))
	} else if len(ranges) > 1 {
		sendSize = rangesMIMESize(ranges, ctype, size)
		code = http.StatusPartialContent

		pr, pw := io.Pipe()
		mw := multipart.NewWriter(pw)
		w.Header().Set("Content-Type", "multipart/byteranges; boundary="+mw.Boundary())
		sendContent = pr
		defer pr.Close() // cause writing goroutine to fail and exit if CopyN doesn't finish.
		go func() {
			for _, ra := range ranges {
				part, err := mw.CreatePart(ra.mimeHeader(ctype, size))
				if err != nil {
					pw.CloseWithError(err)
					return
				}
				if _, err := part.Write(buf[ra.start : ra.start+ra.length]); err != nil {
					pw.CloseWithError(err)
					return
				}
			}
			mw.Close()
			pw.Close()
		}()
	}

	for key, values := range article.Header {
		for _, value := range values {
			w.Header().Add("X-Usenet-"+key, value)
		}
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("ETag", "\""+string(messageID.Short())+"\"")
	w.Header().Set("Content-Length", strconv.FormatInt(sendSize, 10))

	w.WriteHeader(code)

	if r.Method != http.MethodHead {
		if _, err = io.Copy(w, sendContent); err != nil {
			log.Printf("[ERROR] %s %s write error: %s", r.Method, messageID, err.Error())
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	}

	log.Printf("[INFO] %s %s", r.Method, messageID)
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

	s.bufPool = sync.Pool{New: func() any {
		return make([]byte, s.articleSizeLimit)
	}}

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
		var serverCert tls.Certificate
		if serverCert, err = tls.LoadX509KeyPair(s.tlsServerCertFile, s.tlsServerKeyFile); err != nil {
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

// countingWriter counts how many bytes have been written to it.
type countingWriter int64

func (w *countingWriter) Write(p []byte) (n int, err error) {
	*w += countingWriter(len(p))
	return len(p), nil
}

// rangesMIMESize returns the number of bytes it takes to encode the
// provided ranges as a multipart response.
func rangesMIMESize(ranges []httpRange, contentType string, contentSize int64) (encSize int64) {
	var w countingWriter
	mw := multipart.NewWriter(&w)
	for _, ra := range ranges {
		mw.CreatePart(ra.mimeHeader(contentType, contentSize))
		encSize += ra.length
	}
	mw.Close()
	encSize += int64(w)
	return
}
