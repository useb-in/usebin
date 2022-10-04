package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"log"
	"math/rand"
	"time"

	"gopkg.in/nntp.v0"
)

type NNTPServer struct {
	Host        string
	User        string
	Pass        string
	TLS         bool
	Posting     bool
	Connections uint64
}

func (n NNTPServer) newConn() (conn *nntp.Conn, err error) {
	var d nntp.Dialer
	if n.TLS {
		conn, err = d.DialTLS(context.Background(), "tcp", n.Host)
	} else {
		conn, err = d.Dial(context.Background(), "tcp", n.Host)
	}
	if err != nil {
		return
	}
	if n.User != "" {
		if err = conn.CmdAuthinfo(n.User, n.Pass); err != nil {
			conn, _ = nil, conn.Close()
			return
		}
	}
	return
}

type Pool struct {
	servers    []NNTPServer
	getChan    chan *poolGet
	putChan    chan *nntp.Conn
	closeChan  chan *nntp.Conn
	idleExpiry time.Duration
}

var ErrNoMoreServers = errors.New("no more servers")

func NewPool(servers []NNTPServer, idleExpiry time.Duration) *Pool {
	p := &Pool{
		servers:    make([]NNTPServer, len(servers)),
		getChan:    make(chan *poolGet),
		putChan:    make(chan *nntp.Conn),
		closeChan:  make(chan *nntp.Conn),
		idleExpiry: idleExpiry,
	}
	for i := 0; i < len(servers); i++ {
		p.servers[i] = servers[i]
		if p.servers[i].Connections == 0 {
			p.servers[i].Connections = 50
		}
	}
	go p.loop()
	return p
}

func (p *Pool) Get(posting bool, messageID nntp.MessageID, retry int) (conn *nntp.Conn, err error) {
	// pseudo-randomly convert the message ID into a server index so we choose a server uniformly
	// this also makes sure such selection is persistent for subsequent call for the same message ID
	sum := sha256.Sum256([]byte(messageID))
	src := rand.NewSource(int64(binary.LittleEndian.Uint64(sum[:8])))
	r := rand.New(src).Intn(len(p.servers))
	// however if the caller desires a different server, possibly due to content availability issues,
	// iterate through the server list to find another one.
	tries := 0
	ret := make(chan *poolResult)
	for i := 0; i < len(p.servers); i++ {
		n := (i + r) % len(p.servers)
		server := &p.servers[n]
		if server.Posting || !posting {
			tries++
			if tries > retry {
				p.getChan <- &poolGet{i, ret}
				result := <-ret
				conn, err = result.conn, result.err
				return
			}
		}
	}
	// we exausted the server list and no more option is found
	err = ErrNoMoreServers
	return
}

func (p *Pool) Put(conn *nntp.Conn) {
	p.putChan <- conn
}

func (p *Pool) Close(conn *nntp.Conn) (err error) {
	err = conn.Close()
	p.closeChan <- conn
	return
}

type poolGet struct {
	i      int
	result chan<- *poolResult
}

type poolResult struct {
	conn *nntp.Conn
	err  error
}

type poolDeferred struct {
	req  *poolGet
	resp *poolResult
}

type poolIdle struct {
	conn      *nntp.Conn
	idleStart time.Time
}

func (p *Pool) loop() {
	connMap := make(map[*nntp.Conn]int) // map Conn to its server index
	// holds the conns being idle
	idles := make([][]*poolIdle, len(p.servers))
	// counters of created conns both active and idle, for each server
	counters := make([]uint64, len(p.servers))
	queue := make([][]*poolGet, len(p.servers))
	deferredChan := make(chan *poolDeferred)
	processGet := func(req *poolGet) (consumed bool) {
		server := &p.servers[req.i]
		// search for idle conn first
		if len(idles[req.i]) > 0 {
			idle := idles[req.i][0]
			idles[req.i] = idles[req.i][1:]
			req.result <- &poolResult{conn: idle.conn}
			log.Printf("[Pool] %s - REASSIGNED connection, total %d", server.Host, counters[req.i])
			consumed = true
		} else if counters[req.i] < server.Connections {
			// no idle conn, but still has slot left, go secure it
			counters[req.i]++
			// create new conn on another thread
			go func() {
				conn, err := p.servers[req.i].newConn()
				deferredChan <- &poolDeferred{req: req, resp: &poolResult{conn, err}}
			}()
			consumed = true
		}
		return
	}
	processQueue := func(i int) {
		for j := 0; j < len(queue[i]); j++ {
			if !processGet(queue[i][j]) {
				queue[i] = queue[i][j:]
				break
			}
		}
	}
	timer := time.NewTimer(time.Minute)
	for {
		select {
		case get := <-p.getChan:
			// handle Get commands
			if !processGet(get) {
				// slots are full, append to queue
				queue[get.i] = append(queue[get.i], get)
			}

		case conn := <-p.putChan:
			// handle Put commands
			if i, ok := connMap[conn]; ok {
				// check for queued Get requests
				if len(queue[i]) > 0 {
					get := queue[i][0]
					queue[i] = queue[i][1:]
					get.result <- &poolResult{conn: conn}
					log.Printf("[Pool] %s - RECYCLED connection, total %d", p.servers[i].Host, counters[i])
				} else {
					idles[i] = append(idles[i], &poolIdle{conn, time.Now()})
					log.Printf("[Pool] %s - IDLED connection, total %d", p.servers[i].Host, counters[i])
				}
			}

		case conn := <-p.closeChan:
			// handle Close commands
			if i, ok := connMap[conn]; ok {
				counters[i]--
				delete(connMap, conn)
				log.Printf("[Pool] %s - CLOSED connection, total %d", p.servers[i].Host, counters[i])
				processQueue(i)
			}

		case result := <-deferredChan:
			// handle allocation result
			if result.resp.err == nil && result.resp.conn != nil {
				connMap[result.resp.conn] = result.req.i
				log.Printf("[Pool] %s - NEW connection, total %d", p.servers[result.req.i].Host, counters[result.req.i])
			}
			result.req.result <- result.resp
			if result.resp.err != nil {
				// allocation failed, release slot
				log.Printf("[Pool] %s - FAILED connection, total %d", p.servers[result.req.i].Host, counters[result.req.i])
				counters[result.req.i]--
				processQueue(result.req.i)
			}

		case <-timer.C:
			// handle idle purge timer
			expired := time.Now().Add(-p.idleExpiry)
			for i, arr := range idles {
				var newArr []*poolIdle
				for _, idle := range arr {
					if idle.idleStart.After(expired) {
						newArr = append(newArr, idle)
					} else {
						idle.conn.Close()
						counters[i]--
						log.Printf("[Pool] %s - PURGED connection, total %d", p.servers[i].Host, counters[i])
					}
				}
				idles[i] = newArr
			}
			timer = time.NewTimer(time.Minute)
		}
	}
}
