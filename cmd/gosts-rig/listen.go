package main

import (
	"errors"
	"log"
	"net"
	"net/http"
	"sync"
)

// Where the pages are served: --listen's address (this Mac alone, as a
// rule), or, Public (Settings), every address on the same port, so other
// devices on the network can open it (no password: anyone there can). It
// changes while running: the connections open stay, only new ones follow.
type listening struct {
	mu     sync.Mutex
	srv    *http.Server
	ln     net.Listener
	addr   string // listening on now
	normal string // --listen's
}

// listen starts serving srv on addr (--listen's).
func (a *app) listen(srv *http.Server, addr string) error {
	l := &a.net
	l.mu.Lock()
	defer l.mu.Unlock()
	l.srv, l.normal = srv, addr
	return l.to(addr)
}

// to serves on addr in place of where it was (l.mu held): the old address
// is let go first if the new one needs its port, and taken again if the new
// one can't be had.
func (l *listening) to(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil && l.ln != nil {
		l.ln.Close() // (the port's ours: free it, and try again)
		if ln, err = net.Listen("tcp", addr); err != nil {
			if back, e := net.Listen("tcp", l.addr); e == nil {
				l.ln = back
				go l.serve(back)
			}
			return err
		}
	} else if err != nil {
		return err
	} else if l.ln != nil {
		l.ln.Close()
	}
	l.ln, l.addr = ln, addr
	go l.serve(ln)
	return nil
}

func (l *listening) serve(ln net.Listener) {
	if err := l.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
		log.Printf("serving on %s: %v", ln.Addr(), err)
	}
}

// setPublic serves to the network (every address, the same port), or back
// to --listen's address.
func (a *app) setPublic(on bool) error {
	l := &a.net
	l.mu.Lock()
	defer l.mu.Unlock()
	addr := l.normal
	_, port, err := net.SplitHostPort(l.normal)
	if err != nil {
		return err
	}
	if on {
		addr = net.JoinHostPort("0.0.0.0", port)
	} else if isPublic(addr) { // (--listen public already: this Mac alone, then)
		addr = net.JoinHostPort("127.0.0.1", port)
	}
	if addr == l.addr {
		return nil
	}
	if err := l.to(addr); err != nil {
		return err
	}
	log.Printf("listening on %s", addr)
	return nil
}

// isPublic says whether addr is every address (not this Mac's alone).
func isPublic(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	return err == nil && (host == "" || host == "0.0.0.0" || host == "::")
}

// served is where the pages are served now, whether that's public, and the
// addresses other devices can open it at (public: this Mac's on the network).
func (a *app) served() (addr string, public bool, urls []string) {
	a.net.mu.Lock()
	addr = a.net.addr
	a.net.mu.Unlock()
	if !isPublic(addr) {
		return addr, false, nil
	}
	_, port, _ := net.SplitHostPort(addr)
	ifs, _ := net.InterfaceAddrs()
	for _, ia := range ifs {
		if n, ok := ia.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
			urls = append(urls, "http://"+net.JoinHostPort(n.IP.String(), port))
		}
	}
	return addr, true, urls
}

