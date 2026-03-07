// Package metrics provides Prometheus instrumentation for Petri lab lifecycle operations.
package metrics

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Recorder holds all Prometheus metric instances for Petri.
// Use New to create a Recorder bound to a specific prometheus.Registerer.
type Recorder struct {
	labsCreatedTotal   *prometheus.CounterVec
	labsDestroyedTotal *prometheus.CounterVec
	labsActiveGauge    prometheus.Gauge
	createDuration     *prometheus.HistogramVec
	destroyDuration    *prometheus.HistogramVec
}

// New registers Petri metrics with reg and returns a Recorder.
// Pass prometheus.DefaultRegisterer for production use; pass a
// prometheus.NewRegistry() for tests to avoid metric collision.
func New(reg prometheus.Registerer) *Recorder {
	r := &Recorder{}

	r.labsCreatedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "petri",
		Name:      "labs_created_total",
		Help:      "Total number of labs created, by company, level, and cloud provider.",
	}, []string{"company", "level", "provider"})

	r.labsDestroyedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "petri",
		Name:      "labs_destroyed_total",
		Help:      "Total number of labs destroyed, by company, level, cloud provider, and reason.",
	}, []string{"company", "level", "provider", "reason"})

	r.labsActiveGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "petri",
		Name:      "labs_active",
		Help:      "Current number of active labs.",
	})

	r.createDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "petri",
		Name:      "lab_create_duration_seconds",
		Help:      "Duration of lab creation in seconds.",
		Buckets:   []float64{10, 30, 60, 120, 300, 600, 1200},
	}, []string{"company", "level", "provider"})

	r.destroyDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "petri",
		Name:      "lab_destroy_duration_seconds",
		Help:      "Duration of lab destruction in seconds.",
		Buckets:   []float64{5, 15, 30, 60, 120, 300},
	}, []string{"company", "level", "provider"})

	reg.MustRegister(
		r.labsCreatedTotal,
		r.labsDestroyedTotal,
		r.labsActiveGauge,
		r.createDuration,
		r.destroyDuration,
	)

	return r
}

// LabCreated records a successful lab creation event.
func (r *Recorder) LabCreated(company string, level int, provider string) {
	r.labsCreatedTotal.WithLabelValues(company, strconv.Itoa(level), provider).Inc()
	r.labsActiveGauge.Inc()
}

// LabDestroyed records a lab destruction event.
// reason is one of "manual", "ttl_expired", "error".
func (r *Recorder) LabDestroyed(company string, level int, provider, reason string) {
	r.labsDestroyedTotal.WithLabelValues(company, strconv.Itoa(level), provider, reason).Inc()
	r.labsActiveGauge.Dec()
}

// ObserveCreate records the duration of a lab creation.
func (r *Recorder) ObserveCreate(company string, level int, provider string, d time.Duration) {
	r.createDuration.WithLabelValues(company, strconv.Itoa(level), provider).Observe(d.Seconds())
}

// ObserveDestroy records the duration of a lab destruction.
func (r *Recorder) ObserveDestroy(company string, level int, provider string, d time.Duration) {
	r.destroyDuration.WithLabelValues(company, strconv.Itoa(level), provider).Observe(d.Seconds())
}

// StartServer starts an HTTP metrics server on addr (e.g. ":9090").
// It exposes /metrics (Prometheus) and /healthz (liveness probe).
// The server shuts down gracefully when ctx is cancelled.
// StartServer blocks until the server exits.
func StartServer(ctx context.Context, addr string, gatherer prometheus.Gatherer, log *slog.Logger) error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("Metrics server listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("metrics server: %w", err)
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("metrics server shutdown: %w", err)
		}
		return nil
	case err := <-errCh:
		return err
	}
}
