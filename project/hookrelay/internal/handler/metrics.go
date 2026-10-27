package handler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/aurelio/hookrelay/internal/store"
	"github.com/gin-gonic/gin"
)

type MetricsCollector func(context.Context) (store.MetricsSnapshot, error)

var admissionOutcomes = [...]string{
	"accepted", "reused", "backlog_full", "invalid_event", "endpoint_not_found",
	"endpoint_disabled", "idempotency_conflict", "internal_error",
}

type admissionMetric struct {
	Count   uint64
	Seconds float64
}

// Metrics combines process-local admission observations with shared DB state.
// Fixed labels deliberately exclude IDs, URLs, keys and raw error messages.
type Metrics struct {
	collect    MetricsCollector
	maxPending int64
	collecting chan struct{}
	mu         sync.Mutex
	admissions map[string]admissionMetric
}

func NewMetrics(collect MetricsCollector, maxPending int64) *Metrics {
	return &Metrics{
		collect: collect, maxPending: maxPending, collecting: make(chan struct{}, 1),
		admissions: make(map[string]admissionMetric),
	}
}

// ObserveEvents measures the storage admission operation, not JSON decoding,
// authentication or the complete HTTP request. It preserves the original result.
func (m *Metrics) ObserveEvents(create EventCreator) EventCreator {
	return func(ctx context.Context, input store.EventInput) (store.EventResult, error) {
		started := time.Now()
		result, err := create(ctx, input)
		seconds := time.Since(started).Seconds()
		outcome := "internal_error"
		switch {
		case err == nil:
			outcome = "accepted"
			if result.Reused {
				outcome = "reused"
			}
		case errors.Is(err, store.ErrBacklogFull):
			outcome = "backlog_full"
		case errors.Is(err, store.ErrInvalidEvent):
			outcome = "invalid_event"
		case errors.Is(err, store.ErrEndpointNotFound):
			outcome = "endpoint_not_found"
		case errors.Is(err, store.ErrEndpointDisabled):
			outcome = "endpoint_disabled"
		case errors.Is(err, store.ErrIdempotencyConflict):
			outcome = "idempotency_conflict"
		}
		m.mu.Lock()
		metric := m.admissions[outcome]
		metric.Count++
		metric.Seconds += seconds
		m.admissions[outcome] = metric
		m.mu.Unlock()
		return result, err
	}
}

// Get must be mounted behind RequireAPIKey. Failed or overlapping collections
// return 503 instead of publishing fabricated zeros or a partial snapshot.
func (m *Metrics) Get(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	select {
	case m.collecting <- struct{}{}:
		defer func() { <-m.collecting }()
	default:
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "metrics_busy"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	snapshot, err := m.collect(ctx)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "metrics_unavailable"})
		return
	}

	var b bytes.Buffer
	header := func(name, kind, help string) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, kind)
	}
	header("hookrelay_event_admissions_total", "counter", "Storage admission outcomes in this API process; resets on restart.")
	m.mu.Lock()
	observations := make(map[string]admissionMetric, len(m.admissions))
	for outcome, metric := range m.admissions {
		observations[outcome] = metric
	}
	m.mu.Unlock()
	for _, outcome := range admissionOutcomes {
		fmt.Fprintf(&b, "hookrelay_event_admissions_total{outcome=%q} %d\n", outcome, observations[outcome].Count)
	}
	header("hookrelay_event_admission_duration_seconds", "summary", "Storage admission latency in this API process, without quantiles.")
	for _, outcome := range admissionOutcomes {
		metric := observations[outcome]
		fmt.Fprintf(&b, "hookrelay_event_admission_duration_seconds_sum{outcome=%q} %g\n", outcome, metric.Seconds)
		fmt.Fprintf(&b, "hookrelay_event_admission_duration_seconds_count{outcome=%q} %d\n", outcome, metric.Count)
	}

	gauge := func(name, help string, value any) {
		header(name, "gauge", help)
		fmt.Fprintf(&b, "%s %v\n", name, value)
	}
	gauge("hookrelay_events", "Persisted events across all API instances.", snapshot.Events)
	gauge("hookrelay_pending_deliveries_limit", "Configured unfinished delivery capacity; must match across API instances.", m.maxPending)

	writeStates := func(name string, rows []store.StateMetrics, states []string) {
		header(name, "gauge", "Current database rows by state, shared across all processes.")
		byState := make(map[string]store.StateMetrics, len(rows))
		for _, row := range rows {
			byState[row.Status] = row
		}
		var backlog int64
		var oldest float64
		for _, state := range states {
			row := byState[state]
			fmt.Fprintf(&b, "%s{status=%q} %d\n", name, state, row.Count)
			if state == "pending" || state == "processing" || state == "retrying" {
				backlog += row.Count
				oldest = max(oldest, row.OldestAge)
			}
		}
		gauge(name+"_backlog", "Unfinished database rows, including active leases and scheduled retries.", backlog)
		gauge(name+"_oldest_pending_age_seconds", "Age since creation of the oldest unfinished row; zero if empty.", oldest)
	}
	writeStates("hookrelay_deliveries", snapshot.Deliveries, []string{"pending", "processing", "retrying", "succeeded", "failed"})
	writeStates("hookrelay_outbox", snapshot.Outbox, []string{"pending", "processing", "published"})

	attempts := make(map[string]store.AttemptMetrics, len(snapshot.Attempts))
	for _, row := range snapshot.Attempts {
		attempts[row.Status] = row
	}
	// These historical aggregates are gauges, not a histogram/summary: retention
	// can reduce them, and crashed executions have no observed latency/result.
	for _, metric := range []struct {
		name, help string
		value      func(store.AttemptMetrics) any
	}{
		{"hookrelay_finished_attempts", "Persisted finished attempts, including older generations.", func(a store.AttemptMetrics) any { return a.Count }},
		{"hookrelay_attempt_latency_observations", "Finished attempts with recorded latency.", func(a store.AttemptMetrics) any { return a.LatencyCount }},
		{"hookrelay_attempt_latency_seconds", "Sum of recorded attempt latency in seconds.", func(a store.AttemptMetrics) any { return a.LatencySeconds }},
	} {
		header(metric.name, "gauge", metric.help)
		for _, state := range []string{"succeeded", "retrying", "failed"} {
			fmt.Fprintf(&b, "%s{status=%q} %v\n", metric.name, state, metric.value(attempts[state]))
		}
	}
	gauge("hookrelay_unfinished_attempts", "Attempts without a saved result; includes active and abandoned executions.", snapshot.UnfinishedAttempts)
	c.Data(http.StatusOK, "text/plain; version=0.0.4; charset=utf-8", b.Bytes())
}
