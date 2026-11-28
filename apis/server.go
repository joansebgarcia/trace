package apis

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/joansgarcia/trace/hub"
	"github.com/joansgarcia/trace/ingest"
	"github.com/joansgarcia/trace/migrations"
	"github.com/joansgarcia/trace/projects"
	"github.com/joansgarcia/trace/queue"
	"github.com/joansgarcia/trace/store"
	"github.com/joansgarcia/trace/web"
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
	// Process commits a queued Event to data.db, idempotently by Project
	// and event_id, and reports whether it changed data.db. Nil uses the
	// processing of the ingest endpoint.
	Process func(ctx context.Context, data *store.DB, event []byte) (bool, error)
	// TrustedProxies are the CIDRs of the reverse proxies whose
	// X-Forwarded-For and X-Forwarded-Proto headers resolve the IP and
	// scheme of the client. Empty ignores those headers.
	TrustedProxies []netip.Prefix
	// IngestRateLimit is the default rate limit of a Project, in Events/s.
	// Zero uses ingest.DefaultRateLimit.
	IngestRateLimit float64
	// IngestMaxPending is the largest backlog of accepted Events not yet
	// processed. Zero uses ingest.DefaultMaxPending.
	IngestMaxPending int
	// UI holds the files of the web UI, served outside /api/. Nil uses the
	// ones built into the binary: the real UI with the ui tag, a placeholder
	// page without it.
	UI fs.FS
	// Logger receives the server logs. Nil discards them.
	Logger *slog.Logger
}

type Server struct {
	config   Config
	logger   *slog.Logger
	store    *store.Store
	queue    *queue.Queue
	hub      *hub.Hub
	projects *projects.Projects
	ingest   *ingest.Handler
	server   *http.Server
	listener net.Listener
	serveErr chan error
	stopping atomic.Bool
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

// Start opens the data directory, applies the pending migrations, starts the
// queue, then binds the listener and serves in the background.
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

	process := s.config.Process
	if process == nil {
		process = ingest.NewProcess(s.logger)
	}
	h := hub.New()
	q, err := queue.New(queue.Config{
		DB:      st.Ingest,
		Process: h.Wrap(process, st.Data),
		Logger:  s.logger,
	})
	if err != nil {
		return errors.Join(err, st.Close())
	}

	listener, err := net.Listen("tcp", s.config.Addr)
	if err != nil {
		return errors.Join(fmt.Errorf("listen on %s: %w", s.config.Addr, err), q.Close(context.Background()), st.Close())
	}
	s.store = st
	s.queue = q
	s.hub = h
	s.projects = projects.New(st.Data)
	s.listener = listener
	s.ingest = ingest.NewHandler(ingest.Config{
		Projects:   s.projects,
		Queue:      q,
		Logger:     s.logger,
		RateLimit:  s.config.IngestRateLimit,
		MaxPending: s.config.IngestMaxPending,
	})
	ui := s.config.UI
	if ui == nil {
		ui = web.FS
	}
	s.server = &http.Server{
		Handler:           NewRouter(s.logger, s.config.TrustedProxies, s.ping, &s.stopping, q, s.ingest, ui),
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

// Queue returns the queue of accepted Events. It is nil before Start.
func (s *Server) Queue() *queue.Queue {
	return s.queue
}

// Projects returns the Projects of data.db, which resolve Ingest Keys. It is
// nil before Start.
func (s *Server) Projects() *projects.Projects {
	return s.projects
}

// Hub returns the hub notified after commits to data.db. It is nil before
// Start.
func (s *Server) Hub() *hub.Hub {
	return s.hub
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("shutting down")
	s.stopping.Store(true)

	queueErr := s.queue.Close(ctx)
	s.ingest.Close()
	s.hub.Close()
	serverErr := s.server.Shutdown(ctx)
	if serverErr != nil {
		// Cut the requests still in flight before their files close.
		serverErr = errors.Join(serverErr, s.server.Close())
	}
	err := errors.Join(queueErr, serverErr, s.store.Checkpoint(ctx), s.store.Close())
	if err != nil {
		s.logger.Error("shutdown incomplete; queued Events are processed on the next start", "err", err)
		return err
	}
	s.logger.Info("shut down")
	return nil
}

// Run starts the server and blocks until ctx ends or serving fails, then
// shuts down within the configured timeout.
func (s *Server) Run(ctx context.Context) error {
	if err := s.Start(); err != nil {
		return err
	}

	var serveErr error
	select {
	case <-ctx.Done():
	case err, failed := <-s.serveErr:
		if !failed {
			// Serving ends cleanly only after Shutdown, which already ran.
			return nil
		}
		serveErr = fmt.Errorf("serve http: %w", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.config.ShutdownTimeout)
	defer cancel()

	return errors.Join(serveErr, s.Shutdown(shutdownCtx))
}

// ping reports an error unless both files answer and the queue writer runs.
func (s *Server) ping(ctx context.Context) error {
	return errors.Join(s.store.Ping(ctx), s.queue.Ping())
}
