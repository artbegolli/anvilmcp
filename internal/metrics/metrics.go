// Package metrics defines the Prometheus registry and metric handles used
// across the anvilmcp server. All application-level metrics are declared here
// so there's one canonical place to find the names and cardinality contract.
//
// Consumers hold a *Registry and increment metrics directly (e.g.
// r.ToolInvocations.WithLabelValues("fs.read", "success").Inc()). Pass
// NewNoop() in tests that don't care about metrics.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry bundles the Prometheus registry with typed handles for every
// application metric.
type Registry struct {
	reg *prometheus.Registry

	// MCP surface
	ToolInvocations *prometheus.CounterVec
	ToolDuration    *prometheus.HistogramVec
	ResourceReads   *prometheus.CounterVec

	// Policy chokepoints. chokepoint ∈ {"path","exec","env"}, decision ∈ {"allow","deny"}.
	PolicyDecisions *prometheus.CounterVec

	// Exec tool specifics
	ExecConcurrent   prometheus.Gauge
	ExecRateLimited  prometheus.Counter
	ExecOutputBytes  prometheus.Counter
	ExecDuration     prometheus.Histogram
	ExecStreamChunks *prometheus.CounterVec

	// Audit trail. Monitor these: a rising failed count means tool calls are
	// running without being recorded.
	AuditWrites  *prometheus.CounterVec
	AuditSkipped prometheus.Counter
	AuditPruned  prometheus.Counter

	// Reliability
	Panics   *prometheus.CounterVec
	Restarts *prometheus.CounterVec
}

// New constructs a Registry with Go runtime + process collectors registered
// alongside the application metrics.
func New() *Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(collectors.WithGoCollectorRuntimeMetrics(collectors.MetricsAll)),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return build(reg)
}

// NewNoop returns a Registry backed by a private registry that isn't served
// anywhere. Handy in tests.
func NewNoop() *Registry {
	return build(prometheus.NewRegistry())
}

func build(reg *prometheus.Registry) *Registry {
	factory := promauto.With(reg)

	return &Registry{
		reg: reg,

		ToolInvocations: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "anvilmcp_tool_invocations_total",
			Help: "Count of MCP tool invocations, labeled by tool and result.",
		}, []string{"tool", "result"}),

		ToolDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "anvilmcp_tool_duration_seconds",
			Help:    "Latency of MCP tool invocations.",
			Buckets: prometheus.DefBuckets,
		}, []string{"tool"}),

		ResourceReads: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "anvilmcp_resource_reads_total",
			Help: "Count of MCP resource reads, labeled by resource template.",
		}, []string{"template", "result"}),

		PolicyDecisions: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "anvilmcp_policy_decisions_total",
			Help: "Count of policy engine decisions, labeled by chokepoint and decision.",
		}, []string{"chokepoint", "decision"}),

		ExecConcurrent: factory.NewGauge(prometheus.GaugeOpts{
			Name: "anvilmcp_exec_concurrent",
			Help: "Currently executing exec.shell / exec.script invocations.",
		}),

		ExecRateLimited: factory.NewCounter(prometheus.CounterOpts{
			Name: "anvilmcp_exec_rate_limited_total",
			Help: "Count of exec invocations rejected by the rate limiter.",
		}),

		ExecOutputBytes: factory.NewCounter(prometheus.CounterOpts{
			Name: "anvilmcp_exec_output_bytes_total",
			Help: "Total bytes of stdout+stderr captured from exec invocations.",
		}),

		ExecDuration: factory.NewHistogram(prometheus.HistogramOpts{
			Name:    "anvilmcp_exec_duration_seconds",
			Help:    "Wall-clock duration of subprocesses started by exec.shell (exec.script delegates through here). Use anvilmcp_tool_duration_seconds for the tool-facing latency including policy checks.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 5, 15, 60, 300},
		}),

		ExecStreamChunks: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "anvilmcp_exec_stream_chunks_total",
			Help: "Output chunks streamed to clients via notifications/progress, labeled by delivery result. Absence of samples means no client opted into streaming; a rising failed count means chunks are being dropped (streaming is best-effort; the final response is unaffected).",
		}, []string{"result"}), // sent | failed

		AuditWrites: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "anvilmcp_audit_writes_total",
			Help: "Audit trail writes, labeled by op (insert, update, mirror, prune) and result (ok, failed). A non-zero failed count means the audit trail is incomplete: calls are executing without a durable record unless audit.fail_closed is set.",
		}, []string{"op", "result"}),

		AuditSkipped: factory.NewCounter(prometheus.CounterOpts{
			Name: "anvilmcp_audit_skipped_total",
			Help: "Invocations deliberately not recorded because the tool is listed in audit.no_store_tools.",
		}),

		AuditPruned: factory.NewCounter(prometheus.CounterOpts{
			Name: "anvilmcp_audit_pruned_total",
			Help: "Audit rows deleted by audit.retention_days or audit.max_total_bytes enforcement.",
		}),

		Panics: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "anvilmcp_panics_total",
			Help: "Recovered panics, labeled by location (tool, resource, supervisor, ...).",
		}, []string{"location"}),

		Restarts: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "anvilmcp_restarts_total",
			Help: "Supervisor-triggered server restarts, labeled by reason.",
		}, []string{"reason"}), // panic | error
	}
}

// Handler returns an http.Handler that serves the metrics in Prometheus text
// format.
func (r *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(r.reg, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	})
}
