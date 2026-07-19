package observability

import (
	"context"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
)

var safeLabelPattern = regexp.MustCompile(`^[A-Za-z0-9_.:/-]{1,96}$`)

type metricsContextKey struct{}

// WithMetrics binds the process metrics implementation to one request path.
// A nil metrics value is deliberately retained as a no-op binding.
func WithMetrics(ctx context.Context, metrics *Metrics) context.Context {
	return context.WithValue(ctx, metricsContextKey{}, metrics)
}

// MetricsFromContext returns the request-bound metrics implementation, if any.
func MetricsFromContext(ctx context.Context) *Metrics {
	metrics, _ := ctx.Value(metricsContextKey{}).(*Metrics)
	return metrics
}

// AllowedValues contains configured identifiers that are safe to expose as
// metric labels. Tenant IDs are deliberately absent: tenant labels are never
// accepted by this package.
type AllowedValues struct {
	Endpoints             []string
	Models                []string
	Policies              []string
	Windows               []string
	ErrorClasses          []string
	Phases                []string
	Statuses              []string
	Outcomes              []string
	Methods               []string
	OperationStates       []string
	ContinuationDecisions []string
}

type Metrics struct {
	registry *prometheus.Registry
	allowed  labelAllowList
	mu       sync.RWMutex

	activityTotal        *prometheus.CounterVec
	activityFailureTotal *prometheus.CounterVec
	activityDuration     *prometheus.HistogramVec
	providerAttemptTotal *prometheus.CounterVec
	providerDuration     *prometheus.HistogramVec
	serviceClassActual   *prometheus.CounterVec
	budgetAdmission      *prometheus.CounterVec
	budgetReserved       *prometheus.GaugeVec
	costTotal            *prometheus.CounterVec
	costExactTotal       *prometheus.CounterVec
	operationState       *prometheus.CounterVec
	ambiguousTotal       *prometheus.CounterVec
	continuationTotal    *prometheus.CounterVec
	configReloadTotal    *prometheus.CounterVec
	workerPolling        prometheus.Gauge
	heartbeatAge         prometheus.Gauge
}

type labelAllowList struct {
	endpoints, models, policies, windows, errors, phases, statuses,
	outcomes, methods, operationStates, continuationDecisions map[string]struct{}
}

var activityFailureOrigins = map[string]struct{}{
	"worker":   {},
	"provider": {},
	"caller":   {},
	"budget":   {},
}

func values(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		if safeLabelPattern.MatchString(value) {
			result[value] = struct{}{}
		}
	}
	return result
}

func (allowed AllowedValues) lists() labelAllowList {
	return labelAllowList{
		endpoints: values(allowed.Endpoints), models: values(allowed.Models),
		policies: values(allowed.Policies), windows: values(allowed.Windows),
		errors: values(allowed.ErrorClasses), phases: values(allowed.Phases),
		statuses: values(allowed.Statuses), outcomes: values(allowed.Outcomes),
		methods: values(allowed.Methods), operationStates: values(allowed.OperationStates),
		continuationDecisions: values(allowed.ContinuationDecisions),
	}
}

func NewMetrics(allowed AllowedValues) (*Metrics, error) {
	m := &Metrics{registry: prometheus.NewRegistry(), allowed: allowed.lists()}
	m.activityTotal = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "llmtw_activity_total", Help: "Completed and failed Temporal activity calls."}, []string{"status", "error_class"})
	m.activityFailureTotal = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "llmtw_activity_failure_total", Help: "Failed Temporal activity attempts by bounded error origin."}, []string{"origin"})
	m.activityDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "llmtw_activity_duration_seconds", Help: "Activity duration by lifecycle phase."}, []string{"phase"})
	m.providerAttemptTotal = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "llmtw_provider_attempt_total", Help: "Provider attempts by configured route."}, []string{"endpoint", "model", "class", "outcome"})
	m.providerDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "llmtw_provider_duration_seconds", Help: "Provider attempt duration by configured route."}, []string{"endpoint", "model", "class"})
	m.serviceClassActual = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "llmtw_service_class_actual_total", Help: "Requested and actual public service classes."}, []string{"requested", "actual", "endpoint"})
	m.budgetAdmission = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "llmtw_budget_admission_total", Help: "Budget admission outcomes."}, []string{"policy", "outcome"})
	m.budgetReserved = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "llmtw_budget_reserved_micro_usd", Help: "Currently reserved budget in microUSD."}, []string{"policy", "window"})
	m.costTotal = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "llmtw_cost_micro_usd_total", Help: "Accounted cost in integer microUSD."}, []string{"endpoint", "model", "class", "method"})
	m.costExactTotal = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "llmtw_cost_usd_total", Help: "Count of accounted exact-USD cost events; amount remains in the durable ledger."}, []string{"endpoint", "model", "class", "method"})
	m.operationState = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "llmtw_operation_state_total", Help: "Operation state transitions."}, []string{"state"})
	m.ambiguousTotal = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "llmtw_ambiguous_total", Help: "Operations whose provider dispatch is unresolved."}, []string{"endpoint"})
	m.continuationTotal = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "llmtw_continuation_total", Help: "Continuation decisions."}, []string{"decision"})
	m.configReloadTotal = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "llmtw_config_reload_total", Help: "Configuration reload outcomes."}, []string{"outcome"})
	m.workerPolling = prometheus.NewGauge(prometheus.GaugeOpts{Name: "llmtw_worker_polling", Help: "Whether the Temporal worker is polling."})
	m.heartbeatAge = prometheus.NewGauge(prometheus.GaugeOpts{Name: "llmtw_heartbeat_age_seconds", Help: "Age of the most recent Activity heartbeat."})
	collectors := []prometheus.Collector{
		m.activityTotal, m.activityFailureTotal, m.activityDuration, m.providerAttemptTotal, m.providerDuration,
		m.serviceClassActual, m.budgetAdmission, m.budgetReserved, m.costTotal, m.costExactTotal,
		m.operationState, m.ambiguousTotal, m.continuationTotal, m.configReloadTotal,
		m.workerPolling, m.heartbeatAge,
	}
	for _, collector := range collectors {
		if err := m.registry.Register(collector); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (metrics *Metrics) Handler() http.Handler {
	if metrics == nil || metrics.registry == nil {
		return http.NotFoundHandler()
	}
	return promhttp.HandlerFor(metrics.registry, promhttp.HandlerOpts{EnableOpenMetrics: true})
}

func (metrics *Metrics) Gather() ([]*dto.MetricFamily, error) {
	if metrics == nil || metrics.registry == nil {
		return nil, nil
	}
	return metrics.registry.Gather()
}

func (metrics *Metrics) allow(value string, allowed map[string]struct{}) string {
	if !safeLabelPattern.MatchString(value) {
		return "other"
	}
	if len(allowed) == 0 {
		return "other"
	}
	if _, ok := allowed[value]; !ok {
		return "other"
	}
	return value
}

func (metrics *Metrics) builtIn(value string, allowed map[string]struct{}) string {
	if !safeLabelPattern.MatchString(value) {
		return "other"
	}
	if len(allowed) == 0 {
		return value
	}
	if _, ok := allowed[value]; !ok {
		return "other"
	}
	return value
}

func (metrics *Metrics) RecordActivity(status, errorClass string, duration time.Duration, phase string) {
	if metrics == nil {
		return
	}
	metrics.mu.RLock()
	defer metrics.mu.RUnlock()
	metrics.activityTotal.WithLabelValues(metrics.builtIn(status, metrics.allowed.statuses), metrics.builtIn(errorClass, metrics.allowed.errors)).Inc()
	metrics.activityDuration.WithLabelValues(metrics.builtIn(phase, metrics.allowed.phases)).Observe(duration.Seconds())
}

// RecordActivityFailure records a terminal failed Activity attempt. Origin is
// intentionally restricted to the fixed vocabulary.
func (metrics *Metrics) RecordActivityFailure(origin string) {
	if metrics == nil {
		return
	}
	metrics.mu.RLock()
	defer metrics.mu.RUnlock()
	metrics.activityFailureTotal.WithLabelValues(metrics.builtIn(origin, activityFailureOrigins)).Inc()
}

func (metrics *Metrics) RecordProviderAttempt(endpoint, model, class, outcome string, duration time.Duration) {
	if metrics == nil {
		return
	}
	metrics.mu.RLock()
	defer metrics.mu.RUnlock()
	metrics.providerAttemptTotal.WithLabelValues(metrics.allow(endpoint, metrics.allowed.endpoints), metrics.allow(model, metrics.allowed.models), metrics.builtIn(class, map[string]struct{}{"economy": {}, "standard": {}, "priority": {}}), metrics.builtIn(outcome, metrics.allowed.outcomes)).Inc()
	metrics.providerDuration.WithLabelValues(metrics.allow(endpoint, metrics.allowed.endpoints), metrics.allow(model, metrics.allowed.models), metrics.builtIn(class, map[string]struct{}{"economy": {}, "standard": {}, "priority": {}})).Observe(duration.Seconds())
}

func (metrics *Metrics) RecordServiceClass(requested, actual, endpoint string) {
	if metrics == nil {
		return
	}
	metrics.mu.RLock()
	defer metrics.mu.RUnlock()
	classes := map[string]struct{}{"economy": {}, "standard": {}, "priority": {}}
	metrics.serviceClassActual.WithLabelValues(metrics.builtIn(requested, classes), metrics.builtIn(actual, classes), metrics.allow(endpoint, metrics.allowed.endpoints)).Inc()
}

func (metrics *Metrics) RecordBudgetAdmission(policy, outcome string) {
	if metrics == nil {
		return
	}
	metrics.mu.RLock()
	defer metrics.mu.RUnlock()
	metrics.budgetAdmission.WithLabelValues(metrics.allow(policy, metrics.allowed.policies), metrics.builtIn(outcome, metrics.allowed.outcomes)).Inc()
}

func (metrics *Metrics) SetBudgetReserved(policy, window string, microUSD float64) {
	if metrics == nil {
		return
	}
	metrics.mu.RLock()
	defer metrics.mu.RUnlock()
	metrics.budgetReserved.WithLabelValues(metrics.allow(policy, metrics.allowed.policies), metrics.allow(window, metrics.allowed.windows)).Set(microUSD)
}

func (metrics *Metrics) RecordCost(endpoint, model, class, method string, microUSD float64) {
	if metrics == nil {
		return
	}
	metrics.mu.RLock()
	defer metrics.mu.RUnlock()
	classes := map[string]struct{}{"economy": {}, "standard": {}, "priority": {}}
	metrics.costTotal.WithLabelValues(metrics.allow(endpoint, metrics.allowed.endpoints), metrics.allow(model, metrics.allowed.models), metrics.builtIn(class, classes), metrics.allow(method, metrics.allowed.methods)).Add(microUSD)
}

// RecordExactCost records an exact-USD accounting event without converting
// the amount to float. The exact decimal remains in the response/ledger;
// Prometheus only receives a bounded event count.
func (metrics *Metrics) RecordExactCost(endpoint, model, class, method string) {
	if metrics == nil {
		return
	}
	metrics.mu.RLock()
	defer metrics.mu.RUnlock()
	classes := map[string]struct{}{"economy": {}, "standard": {}, "priority": {}}
	metrics.costExactTotal.WithLabelValues(metrics.allow(endpoint, metrics.allowed.endpoints), metrics.allow(model, metrics.allowed.models), metrics.builtIn(class, classes), metrics.allow(method, metrics.allowed.methods)).Inc()
}

func (metrics *Metrics) RecordOperationState(state string) {
	if metrics == nil {
		return
	}
	metrics.mu.RLock()
	defer metrics.mu.RUnlock()
	metrics.operationState.WithLabelValues(metrics.allow(state, metrics.allowed.operationStates)).Inc()
}

func (metrics *Metrics) RecordAmbiguous(endpoint string) {
	if metrics == nil {
		return
	}
	metrics.mu.RLock()
	defer metrics.mu.RUnlock()
	metrics.ambiguousTotal.WithLabelValues(metrics.allow(endpoint, metrics.allowed.endpoints)).Inc()
}

func (metrics *Metrics) RecordContinuation(decision string) {
	if metrics == nil {
		return
	}
	metrics.mu.RLock()
	defer metrics.mu.RUnlock()
	metrics.continuationTotal.WithLabelValues(metrics.allow(decision, metrics.allowed.continuationDecisions)).Inc()
}

func (metrics *Metrics) RecordConfigReload(outcome string) {
	if metrics == nil {
		return
	}
	metrics.configReloadTotal.WithLabelValues(metrics.builtIn(outcome, metrics.allowed.outcomes)).Inc()
}

func (metrics *Metrics) SetWorkerPolling(polling bool) {
	if metrics == nil {
		return
	}
	if polling {
		metrics.workerPolling.Set(1)
	} else {
		metrics.workerPolling.Set(0)
	}
}

func (metrics *Metrics) SetHeartbeatAge(age time.Duration) {
	if metrics == nil {
		return
	}
	if age < 0 {
		age = 0
	}
	metrics.heartbeatAge.Set(age.Seconds())
}
