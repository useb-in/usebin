package main

import (
	"bytes"
	"crypto/tls"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/nntp.v0"
	"gopkg.in/pwgen.v0"
	"gopkg.in/textproto.v0"
)

type server struct {
	Host             string
	Port             uint16
	NNTPServers      []NNTPServer
	IdleConnExpiry   int64
	DefaultNewsgroup string
	ArticleSizeLimit uint64
	CertFile         string
	KeyFile          string
	pool             *Pool
	bufPool          sync.Pool
}

//go:embed static
var staticFS embed.FS

type Entity int

const (
	Static Entity = iota
	FullArticle
	ArticleHead
)

func (s *server) handleMessage(staticHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var (
			entity     Entity
			dotEncoded bool
			messageID  nntp.MessageID
		)

		// https://developers.cloudflare.com/cache/about/default-cache-behavior/#default-cached-file-extensions
		if name := r.URL.Path[3:]; !strings.HasSuffix(name, ".csv") && !strings.HasSuffix(name, ".nfo") {
			w.WriteHeader(http.StatusBadRequest)
			return
		} else if messageID = nntp.MessageID(name[:len(name)-4]); messageID.Validate() != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		switch r.URL.Path[:3] {
		case "/m/":
			entity = FullArticle
			dotEncoded = false
		case "/d/":
			entity = FullArticle
			dotEncoded = true
		case "/h/":
			entity = ArticleHead
		default:
			entity = Static
		}

		// general headers
		w.Header().Set("Cache-Control", "public, max-age=2592000")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("X-Content-Type-Options", "nosniff")

		switch entity {
		case FullArticle:
			if r.Method == http.MethodGet && dotEncoded {
				s.handleDotEncodedMessageGET(w, r, messageID)
				return
			}
			switch r.Method {
			case http.MethodGet, http.MethodHead:
				s.handleMessageGET(w, r, messageID)
			case http.MethodPost:
				s.handleMessagePOST(w, r, messageID, dotEncoded)
			}
		case ArticleHead:
			s.handleMessageHead(w, r, messageID)
		default:
			staticHandler.ServeHTTP(w, r)
		}
	})
}

func (s *server) handleDotEncodedMessageGET(w http.ResponseWriter, r *http.Request, messageID nntp.MessageID) {
	var (
		err     error
		nntpErr *nntp.Error
		conn    *nntp.Conn
		article *nntp.Article
		done    bool
		found   bool
		retries int
	)

	ctype := "text/plain; charset=utf-8"

	if done, _ = checkPreconditions(w, r); done {
		return
	}

	defer func() {
		if conn != nil {
			if err == nil || errors.As(err, &nntpErr) {
				// NNTP error, connection still intact, don't throw away the conn
				s.pool.Put(conn)
			} else {
				s.pool.Close(conn)
			}
		}
	}()

	for found, retries = false, 0; !found; retries++ {
		if conn, err = s.pool.Get(false, messageID, retries); errors.Is(err, ErrNoMoreServers) {
			break
		} else if err != nil {
			log.Printf("[ERROR] %s (RAW) %s pool error: %s", r.Method, messageID, err.Error())
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if article, err = conn.CmdArticle(nntp.ArticleMessageID(messageID), nntp.WithDotEncodedBody()); err != nil {
			if errors.As(err, &nntpErr) {
				s.pool.Put(conn)
				continue
			}
			log.Printf("[ERROR] %s (RAW) %s connection error: %s", r.Method, messageID, err.Error())
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		found = true
	}

	if !found {
		log.Printf("[ERROR] %s (RAW) %s not found", r.Method, messageID)
		w.WriteHeader(http.StatusNotFound)
		return
	}

	for key, values := range article.Header {
		switch strings.ToLower(key) {
		case "organization", "x-complaints-to":
			continue
		}
		for _, value := range values {
			w.Header().Add("X-Usenet-"+key, value)
		}
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("ETag", "\""+string(messageID.Short())+"\"")

	w.WriteHeader(http.StatusOK)

	if _, err = io.Copy(w, io.LimitReader(article.Body, int64(s.ArticleSizeLimit))); err != nil {
		log.Printf("[ERROR] %s (RAW) %s write error: %s", r.Method, messageID, err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	log.Printf("[INFO] %s (RAW) %s", r.Method, messageID)
}

func (s *server) handleMessageGET(w http.ResponseWriter, r *http.Request, messageID nntp.MessageID) {
	var (
		err         error
		nntpErr     *nntp.Error
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
		found       bool
		retries     int
	)

	ctype := "text/plain; charset=utf-8"

	if done, rangeReq = checkPreconditions(w, r); done {
		return
	}

	v = s.bufPool.Get()
	defer s.bufPool.Put(v)
	buf = v.([]byte)

	defer func() {
		if conn != nil {
			if err == nil || errors.As(err, &nntpErr) {
				// NNTP error, connection still intact, don't throw away the conn
				s.pool.Put(conn)
			} else {
				s.pool.Close(conn)
			}
		}
	}()

	for found, retries = false, 0; !found; retries++ {
		if conn, err = s.pool.Get(false, messageID, retries); errors.Is(err, ErrNoMoreServers) {
			break
		} else if err != nil {
			log.Printf("[ERROR] %s %s pool error: %s", r.Method, messageID, err.Error())
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if article, err = conn.CmdArticle(nntp.ArticleMessageID(messageID)); err != nil {
			if errors.As(err, &nntpErr) {
				s.pool.Put(conn)
				continue
			}
			log.Printf("[ERROR] %s %s connection error: %s", r.Method, messageID, err.Error())
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		found = true
	}

	if !found {
		log.Printf("[ERROR] %s %s not found", r.Method, messageID)
		w.WriteHeader(http.StatusNotFound)
		return
	}

	if n, err = io.ReadFull(article.Body, buf); err == io.ErrUnexpectedEOF {
		err = nil
	} else if err != nil {
		log.Printf("[ERROR] %s %s read error: %s", r.Method, messageID, err.Error())
		w.WriteHeader(http.StatusInternalServerError)
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
		switch strings.ToLower(key) {
		case "organization", "x-complaints-to":
			continue
		}
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

func (s *server) handleMessagePOST(w http.ResponseWriter, r *http.Request, messageID nntp.MessageID, dotEncoded bool) {
	var (
		err     error
		nntpErr *nntp.Error
		conn    *nntp.Conn
		ngID    string
	)

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
			header.Set("Newsgroups", s.DefaultNewsgroup)
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
	article := &nntp.Article{
		MessageID: messageID,
		Header:    header,
		Body:      io.LimitReader(r.Body, int64(s.ArticleSizeLimit)),
	}

	defer func() {
		if conn != nil {
			if err == nil || errors.As(err, &nntpErr) {
				// NNTP error, connection still intact, don't throw away the conn
				s.pool.Put(conn)
			} else {
				s.pool.Close(conn)
			}
		}
	}()

	if conn, err = s.pool.Get(true, messageID, 0); err != nil {
		if errors.Is(err, ErrNoMoreServers) {
			log.Printf("[ERROR] %s %s no posting servers?", r.Method, messageID)
			return
		} else {
			log.Printf("[ERROR] %s %s pool error: %s", r.Method, messageID, err.Error())
		}
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if dotEncoded {
		err = conn.CmdPost(article, nntp.WithDotEncodedBody())
	} else {
		err = conn.CmdPost(article)
	}

	if err != nil {
		if errors.Is(err, nntp.ResponseCodePostingFailure) {
			w.WriteHeader(http.StatusConflict)
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}
		log.Printf("[ERROR] %s %s error: %s", r.Method, messageID, err.Error())
		return
	}

	w.WriteHeader(http.StatusOK)
	log.Printf("[INFO] POST %s", messageID)
}

func (s *server) handleMessageHead(w http.ResponseWriter, r *http.Request, messageID nntp.MessageID) {
	var (
		err     error
		nntpErr *nntp.Error
		conn    *nntp.Conn
		article *nntp.Article
		done    bool
		found   bool
		retries int
	)

	ctype := "text/plain; charset=utf-8"

	if done, _ = checkPreconditions(w, r); done {
		return
	}

	defer func() {
		if conn != nil {
			if err == nil || errors.As(err, &nntpErr) {
				// NNTP error, connection still intact, don't throw away the conn
				s.pool.Put(conn)
			} else {
				s.pool.Close(conn)
			}
		}
	}()

	for found, retries = false, 0; !found; retries++ {
		if conn, err = s.pool.Get(false, messageID, retries); errors.Is(err, ErrNoMoreServers) {
			break
		} else if err != nil {
			log.Printf("[ERROR] %s %s pool error: %s", r.Method, messageID, err.Error())
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if article, err = conn.CmdHead(nntp.ArticleMessageID(messageID)); err != nil {
			if errors.As(err, &nntpErr) {
				s.pool.Put(conn)
				continue
			}
			log.Printf("[ERROR] %s %s connection error: %s", r.Method, messageID, err.Error())
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		found = true
	}

	if !found {
		log.Printf("[ERROR] %s %s not found", r.Method, messageID)
		w.WriteHeader(http.StatusNotFound)
		return
	}

	for key, values := range article.Header {
		switch strings.ToLower(key) {
		case "organization", "x-complaints-to":
			continue
		}
		for _, value := range values {
			w.Header().Add("X-Usenet-"+key, value)
		}
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("ETag", "\""+string(messageID.Short())+"\"")

	w.WriteHeader(http.StatusOK)

	log.Printf("[INFO] HEAD %s", messageID)
}

func (s *server) Serve() (err error) {
	if len(s.NNTPServers) == 0 {
		err = fmt.Errorf("no NNTP server definitions")
		return
	}
	if s.Host == "" {
		s.Host = "0.0.0.0"
	}
	if s.Port == 0 {
		s.Port = 80
	}
	if s.IdleConnExpiry == 0 {
		s.IdleConnExpiry = 60
	}
	if s.DefaultNewsgroup == "" {
		s.DefaultNewsgroup = "alt.binaries.misc"
	}
	if s.ArticleSizeLimit == 0 {
		s.ArticleSizeLimit = 4 * 1024 * 1024 // 4MB
	}

	s.bufPool = sync.Pool{New: func() any {
		return make([]byte, s.ArticleSizeLimit)
	}}

	s.pool = NewPool(s.NNTPServers, time.Second*time.Duration(s.IdleConnExpiry))

	subFS, err := fs.Sub(staticFS, "static")
	if err != nil {
		return
	}
	httpFS := http.FS(subFS)
	fileServer := http.FileServer(httpFS)
	serveIndex := serveFileContents("index.html", httpFS)
	staticHandler := intercept404(fileServer, serveIndex)
	mainHandler := s.handleMessage(staticHandler)

	httpServer := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", s.Host, s.Port),
		Handler: mainHandler,
	}

	if s.CertFile != "" && s.KeyFile != "" {
		var serverCert tls.Certificate
		if serverCert, err = tls.LoadX509KeyPair(s.CertFile, s.KeyFile); err != nil {
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
