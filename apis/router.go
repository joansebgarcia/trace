package apis

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
)

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(status int) {
	s.status = status
	s.ResponseWriter.WriteHeader(status)
}

// NewRouter serves the HTTP API. ping reports whether the instance is
// healthy; once stopping is set, every request gets 503 shutting_down.
// Requests from proxies resolve their client through the forwarding headers.
func NewRouter(logger *slog.Logger, proxies []netip.Prefix, ping func(context.Context) error, stopping *atomic.Bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", serveHealth(logger, ping))

	return logRequests(logger, proxies, rejectWhileStopping(stopping, mux))
}

func rejectWhileStopping(stopping *atomic.Bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if stopping.Load() {
			w.Header().Set("Connection", "close")
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "shutting_down"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func serveHealth(logger *slog.Logger, ping func(context.Context) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := ping(r.Context()); err != nil {
			logger.Warn("health check failed", "err", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func logRequests(logger *slog.Logger, proxies []netip.Prefix, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, scheme := resolveClient(r, proxies)
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		logger.Debug("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"client_ip", ip,
			"scheme", scheme,
		)
	})
}

// resolveClient returns the IP and scheme of the client behind r. The
// forwarding headers count only when the peer is one of proxies; then the
// client is the nearest X-Forwarded-For hop that is not a proxy, so hops a
// client prepends itself cannot spoof it.
func resolveClient(r *http.Request, proxies []netip.Prefix) (ip, scheme string) {
	peer := parseAddr(r.RemoteAddr)
	scheme = "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if !peer.IsValid() {
		return r.RemoteAddr, scheme
	}
	if !isProxy(peer, proxies) {
		return peer.String(), scheme
	}

	client := peer
	hops := splitValues(r.Header.Values("X-Forwarded-For"))
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(hops[i])
		if err != nil {
			break
		}
		client = hop.Unmap()
		if !isProxy(client, proxies) {
			break
		}
	}

	// The rightmost value comes from the nearest proxy, which is trusted.
	protos := splitValues(r.Header.Values("X-Forwarded-Proto"))
	switch proto := strings.ToLower(protos[len(protos)-1]); proto {
	case "http", "https":
		scheme = proto
	}
	return client.String(), scheme
}

// splitValues returns the comma-separated values of every line of a header,
// in order. It returns at least one value.
func splitValues(lines []string) []string {
	values := strings.Split(strings.Join(lines, ","), ",")
	for i, value := range values {
		values[i] = strings.TrimSpace(value)
	}
	return values
}

// parseAddr returns the IP of a host:port address, or the zero Addr.
func parseAddr(hostport string) netip.Addr {
	addrPort, err := netip.ParseAddrPort(hostport)
	if err != nil {
		return netip.Addr{}
	}
	return addrPort.Addr().Unmap()
}

func isProxy(ip netip.Addr, proxies []netip.Prefix) bool {
	for _, proxy := range proxies {
		if proxy.Contains(ip) {
			return true
		}
	}
	return false
}
