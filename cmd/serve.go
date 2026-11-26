package cmd

import (
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/joansebgarcia/amber/apis"
)

const (
	defaultAddr            = "127.0.0.1:8090"
	defaultLogFormat       = "text"
	defaultLogLevel        = "info"
	defaultShutdownTimeout = 10 * time.Second
)

var logHandlers = map[string]func(io.Writer, *slog.HandlerOptions) slog.Handler{
	"text": func(w io.Writer, o *slog.HandlerOptions) slog.Handler { return slog.NewTextHandler(w, o) },
	"json": func(w io.Writer, o *slog.HandlerOptions) slog.Handler { return slog.NewJSONHandler(w, o) },
}

var logLevels = map[string]slog.Level{
	"debug": slog.LevelDebug,
	"info":  slog.LevelInfo,
	"warn":  slog.LevelWarn,
	"error": slog.LevelError,
}

func newServe() *cobra.Command {
	var (
		config    apis.Config
		logFormat string
		logLevel  string
	)

	serve := &cobra.Command{
		Use:   "serve",
		Short: "Run the server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			logger, err := newLogger(cmd.OutOrStdout(), logFormat, logLevel)
			if err != nil {
				return err
			}
			config.Logger = logger
			logger.Info("starting",
				"version", cmd.Root().Version,
				"http", config.Addr,
				"log_format", logFormat,
				"log_level", logLevel,
				"shutdown_timeout", config.ShutdownTimeout.String(),
			)

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			return apis.NewServer(config).Run(ctx)
		},
	}

	flags := serve.Flags()
	flags.StringVar(&config.Addr, "http", defaultAddr, "TCP address to listen on for HTTP")
	flags.StringVar(&logFormat, "log-format", defaultLogFormat, "log format: "+listLogFormats())
	flags.StringVar(&logLevel, "log-level", defaultLogLevel, "minimum log level: "+listLogLevels())
	flags.DurationVar(&config.ShutdownTimeout, "shutdown-timeout", defaultShutdownTimeout, "maximum time to wait for a graceful shutdown")

	return serve
}

func newLogger(w io.Writer, format, level string) (*slog.Logger, error) {
	minLevel, ok := logLevels[level]
	if !ok {
		return nil, fmt.Errorf("invalid log level %q: use %s", level, listLogLevels())
	}

	handler, ok := logHandlers[format]
	if !ok {
		return nil, fmt.Errorf("invalid log format %q: use %s", format, listLogFormats())
	}

	return slog.New(handler(w, &slog.HandlerOptions{Level: minLevel})), nil
}

func listLogFormats() string {
	return strings.Join(slices.Sorted(maps.Keys(logHandlers)), " or ")
}

func listLogLevels() string {
	names := slices.SortedFunc(maps.Keys(logLevels), func(a, b string) int {
		return int(logLevels[a] - logLevels[b])
	})
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}
