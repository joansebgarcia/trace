package apis

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/joansebgarcia/amber/migrations"
	"github.com/joansebgarcia/amber/store"
)

const readHeaderTimeout = 10 * time.Second

type Config struct {
	Addr string
	// DataDir is the data directory. It is created if it does not exist.
	DataDir         string
	ShutdownTimeout time.Duration
	// Migrations are the schema migrations applied on Start. Nil uses the
	// ones built into the binary.
	Migrations *store.Migrations
	// Logger receives the server logs. Nil discards them.
	Logger *slog.Logger
}

type Server struct {
	config   Config
	logger   *slog.Logger
	store    *store.Store
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
		config:   config,
		logger:   logger,
		serveErr: make(chan error, 1),
	}
}

// Start opens the data directory, applies the pending migrations, then binds
// the listener and serves in the background.
func (s *Server) Start() error {
	st, err := store.Open(s.config.DataDir)
	if err != nil {
		return err
	}
	s.logger.Info("data directory open", "dir", s.config.DataDir)

	schema := s.config.Migrations
	if schema == nil {
		if schema, err = store.LoadMigrations(migrations.Files); err != nil {
			return errors.Join(err, st.Close())
		}
	}
	if err := st.Migrate(context.Background(), schema); err != nil {
		return errors.Join(err, st.Close())
	}

	listener, err := net.Listen("tcp", s.config.Addr)
	if err != nil {
		return errors.Join(fmt.Errorf("listen on %s: %w", s.config.Addr, err), st.Close())
	}
	s.store = st
	s.listener = listener
	s.server = &http.Server{
		Handler:           NewRouter(s.logger, st),
		ReadHeaderTimeout: readHeaderTimeout,
	}
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

// Store returns the open data directory. It is nil before Start.
func (s *Server) Store() *store.Store {
	return s.store
}

// Shutdown waits for in-flight requests until ctx ends, then closes the data
// directory.
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("shutting down")
	return errors.Join(s.server.Shutdown(ctx), s.store.Close())
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
			return errors.Join(fmt.Errorf("serve http: %w", err), s.store.Close())
		}
		// Serving ends cleanly only after Shutdown, which already closed the
		// store.
		return nil
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.config.ShutdownTimeout)
	defer cancel()

	return s.Shutdown(shutdownCtx)
}
