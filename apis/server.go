package apis

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

const readHeaderTimeout = 10 * time.Second

type Config struct {
	Addr            string
	ShutdownTimeout time.Duration
	// Logger receives the server logs. Nil discards them.
	Logger *slog.Logger
}

type Server struct {
	config   Config
	logger   *slog.Logger
	server   *http.Server
	listener net.Listener
	serveErr chan error
}

func NewServer(config Config) *Server {
	logger := config.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	return &Server{
		config: config,
		logger: logger,
		server: &http.Server{
			Handler:           NewRouter(logger),
			ReadHeaderTimeout: readHeaderTimeout,
		},
		serveErr: make(chan error, 1),
	}
}

// Start binds the listener and serves in the background.
func (s *Server) Start() error {
	listener, err := net.Listen("tcp", s.config.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.config.Addr, err)
	}
	s.listener = listener
	s.logger.Info("http server listening", "addr", s.Addr())

	go func() {
		if err := s.server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
			s.serveErr <- err
		}
		close(s.serveErr)
	}()

	return nil
}

// Addr returns the address the listener is bound to.
func (s *Server) Addr() string {
	return s.listener.Addr().String()
}

// Shutdown waits for in-flight requests until ctx ends.
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("shutting down")
	return s.server.Shutdown(ctx)
}

// Run starts the server and blocks until ctx ends or serving fails, then
// shuts down within the configured timeout.
func (s *Server) Run(ctx context.Context) error {
	if err := s.Start(); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
	case err, failed := <-s.serveErr:
		if failed {
			return fmt.Errorf("serve http: %w", err)
		}
		return nil
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.config.ShutdownTimeout)
	defer cancel()

	return s.Shutdown(shutdownCtx)
}
