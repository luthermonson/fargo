package controller

import (
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
)

// metrics is a tiny, dependency-free set of counters exposed in Prometheus
// text format. A prototype does not need a full client library.
type metrics struct {
	begun      atomic.Int64
	committed  atomic.Int64
	rolledBack atomic.Int64
	errors     atomic.Int64
}

// The metrics type implements reconcile.Observer.
func (m *metrics) Begun(string)      { m.begun.Add(1) }
func (m *metrics) Committed(string)  { m.committed.Add(1) }
func (m *metrics) RolledBack(string) { m.rolledBack.Add(1) }
func (m *metrics) Errored(string)    { m.errors.Add(1) }

func (m *metrics) write(w http.ResponseWriter) {
	fmt.Fprintf(w, "# HELP rewind_transactions_begun_total Transactions begun.\n")
	fmt.Fprintf(w, "# TYPE rewind_transactions_begun_total counter\n")
	fmt.Fprintf(w, "rewind_transactions_begun_total %d\n", m.begun.Load())
	fmt.Fprintf(w, "# HELP rewind_transactions_committed_total Transactions committed.\n")
	fmt.Fprintf(w, "# TYPE rewind_transactions_committed_total counter\n")
	fmt.Fprintf(w, "rewind_transactions_committed_total %d\n", m.committed.Load())
	fmt.Fprintf(w, "# HELP rewind_transactions_rolledback_total Transactions rolled back.\n")
	fmt.Fprintf(w, "# TYPE rewind_transactions_rolledback_total counter\n")
	fmt.Fprintf(w, "rewind_transactions_rolledback_total %d\n", m.rolledBack.Load())
	fmt.Fprintf(w, "# HELP rewind_errors_total Reconcile errors.\n")
	fmt.Fprintf(w, "# TYPE rewind_errors_total counter\n")
	fmt.Fprintf(w, "rewind_errors_total %d\n", m.errors.Load())
}

// serveMetrics starts the metrics/health HTTP server. It returns once the
// listener is bound; the server stops when ctx-driven shutdown closes it.
func serveMetrics(addr string, m *metrics) (*http.Server, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		m.write(w)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("bind metrics address %q: %w", addr, err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	return srv, nil
}
