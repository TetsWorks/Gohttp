package gohttp

import (
	"crypto/tls"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/TetsWorks/Gohttp/internal/metrics"
	"github.com/TetsWorks/Gohttp/internal/middleware"
	"github.com/TetsWorks/Gohttp/internal/parser"
	"github.com/TetsWorks/Gohttp/internal/router"
)

// Server é o servidor HTTP/1.1 do zero
type Server struct {
	Router          *router.Router
	Metrics         *metrics.Collector
	TLSConfig       *tls.Config
	Addr            string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	MaxConns        int
	ShutdownTimeout time.Duration
	listener        net.Listener
	wg              sync.WaitGroup
	quit            chan struct{}
	connSem         chan struct{}
}

func New(addr string) *Server {
	s := &Server{
		Router:          router.New(),
		Metrics:         metrics.New(),
		Addr:            addr,
		ReadTimeout:     30 * time.Second,
		WriteTimeout:    30 * time.Second,
		IdleTimeout:     120 * time.Second,
		MaxConns:        10000,
		ShutdownTimeout: 30 * time.Second,
		quit:            make(chan struct{}),
	}
	s.connSem = make(chan struct{}, s.MaxConns)
	s.Router.Use(middleware.Recover(), s.Metrics.Middleware(), middleware.Logger())
	s.Router.GET("/_metrics", s.Metrics.DashboardHandler())
	s.Router.GET("/_metrics/json", s.Metrics.DashboardHandler())
	return s
}

func (s *Server) Listen() error {
	ln, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return fmt.Errorf("gohttp: listen %s: %w", s.Addr, err)
	}
	s.listener = ln
	fmt.Printf("\033[1;32m⚡ GoHTTP\033[0m → \033[1;34mhttp://%s\033[0m\n", s.Addr)
	fmt.Printf("   metrics: \033[90mhttp://%s/_metrics\033[0m\n", s.Addr)
	return s.serve(ln)
}

func (s *Server) ListenTLS(certFile, keyFile string) error {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return err
	}
	s.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	ln, err := tls.Listen("tcp", s.Addr, s.TLSConfig)
	if err != nil {
		return err
	}
	s.listener = ln
	fmt.Printf("\033[1;32m🔒 GoHTTP TLS\033[0m → \033[1;34mhttps://%s\033[0m\n", s.Addr)
	return s.serve(ln)
}

func (s *Server) ListenTLSAuto() error {
	cert, err := generateSelfSigned()
	if err != nil {
		return err
	}
	s.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	ln, err := tls.Listen("tcp", s.Addr, s.TLSConfig)
	if err != nil {
		return err
	}
	s.listener = ln
	fmt.Printf("\033[1;33m🔒 GoHTTP TLS auto\033[0m → \033[1;34mhttps://%s\033[0m\n", s.Addr)
	return s.serve(ln)
}

func (s *Server) serve(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-s.quit:
				return nil
			default:
				if ne, ok := err.(net.Error); ok && ne.Temporary() {
					time.Sleep(5 * time.Millisecond)
					continue
				}
				return err
			}
		}
		select {
		case s.connSem <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		s.Metrics.IncrConns()
		s.wg.Add(1)
		go func(c net.Conn) {
			defer func() { c.Close(); s.Metrics.DecrConns(); s.wg.Done(); <-s.connSem }()
			s.handleConn(c)
		}(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	p := parser.New(conn)
	for {
		if s.ReadTimeout > 0 {
			conn.SetReadDeadline(time.Now().Add(s.ReadTimeout))
		}
		req, err := p.ParseRequest()
		if err != nil {
			return
		}
		if s.WriteTimeout > 0 {
			conn.SetWriteDeadline(time.Now().Add(s.WriteTimeout))
		}
		resp := parser.NewResponse(conn, req)
		s.Router.ServeHTTP(resp, req)
		resp.Flush()
		if !req.IsKeepAlive {
			return
		}
		if s.IdleTimeout > 0 {
			conn.SetDeadline(time.Now().Add(s.IdleTimeout))
		}
	}
}

func (s *Server) Shutdown() {
	close(s.quit)
	if s.listener != nil {
		s.listener.Close()
	}
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(s.ShutdownTimeout):
	}
}

func (s *Server) GET(path string, h router.HandlerFunc, mws ...router.Middleware) {
	s.Router.GET(path, h, mws...)
}
func (s *Server) POST(path string, h router.HandlerFunc, mws ...router.Middleware) {
	s.Router.POST(path, h, mws...)
}
func (s *Server) PUT(path string, h router.HandlerFunc, mws ...router.Middleware) {
	s.Router.PUT(path, h, mws...)
}
func (s *Server) DELETE(path string, h router.HandlerFunc, mws ...router.Middleware) {
	s.Router.DELETE(path, h, mws...)
}
func (s *Server) PATCH(path string, h router.HandlerFunc, mws ...router.Middleware) {
	s.Router.PATCH(path, h, mws...)
}
func (s *Server) Group(prefix string, mws ...router.Middleware) *router.Group {
	return s.Router.Group(prefix, mws...)
}
func (s *Server) Use(mws ...router.Middleware) { s.Router.Use(mws...) }
