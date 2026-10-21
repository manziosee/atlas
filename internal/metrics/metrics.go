// Package metrics defines Atlas' Prometheus instrumentation. All collectors
// are registered on a dedicated registry (not the global default) so that
// only intentional metrics are exposed.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const namespace = "atlas"

// Registry holds every Atlas collector.
var Registry = prometheus.NewRegistry()

var factory = promauto.With(Registry)

var (
	// Transport
	PacketsSent = factory.NewCounter(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "transport", Name: "packets_sent_total",
		Help: "Gossip packets sent.",
	})
	PacketsReceived = factory.NewCounter(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "transport", Name: "packets_received_total",
		Help: "Gossip packets received and successfully authenticated.",
	})
	PacketsRejected = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "transport", Name: "packets_rejected_total",
		Help: "Gossip packets dropped, by reason (decrypt, decode, clock_skew, backpressure, admission, oversize).",
	}, []string{"reason"})
	BytesSent = factory.NewCounter(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "transport", Name: "bytes_sent_total",
		Help: "Gossip bytes sent (after encryption).",
	})
	BytesReceived = factory.NewCounter(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "transport", Name: "bytes_received_total",
		Help: "Gossip bytes received (before decryption).",
	})

	// Membership
	Members = factory.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "membership", Name: "members",
		Help: "Known cluster members by state (including self).",
	}, []string{"state"})
	Probes = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "membership", Name: "probes_total",
		Help: "Failure-detector probes by result (ack, indirect_ack, timeout).",
	}, []string{"result"})
	ProbeRTT = factory.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "membership", Name: "probe_rtt_seconds",
		Help:    "Round-trip time of successful probes.",
		Buckets: prometheus.ExponentialBuckets(0.0005, 2, 14), // 0.5ms .. ~4s
	})
	PeerRTT = factory.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "membership", Name: "peer_rtt_seconds",
		Help: "Per-peer exponentially weighted moving average of successful probe round-trip time.",
	}, []string{"member"})
	StateTransitions = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "membership", Name: "state_transitions_total",
		Help: "Member state transitions observed, by new state.",
	}, []string{"to"})
	Refutations = factory.NewCounter(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "membership", Name: "refutations_total",
		Help: "Times this node refuted a suspicion about itself.",
	})
	BroadcastQueue = factory.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "membership", Name: "broadcast_queue",
		Help: "Pending member-state broadcasts awaiting epidemic retransmission.",
	})
	BroadcastsDropped = factory.NewCounter(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "membership", Name: "broadcasts_dropped_total",
		Help: "Member-state broadcasts discarded because the bounded queue was full.",
	})
	LocalHealthScore = factory.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "membership", Name: "local_health_score",
		Help: "Lifeguard local health score; higher values lengthen this node's probe deadline.",
	})

	// HTTP API
	HTTPRequests = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "http", Name: "requests_total",
		Help: "HTTP requests by route, method and status code.",
	}, []string{"route", "method", "code"})
	HTTPDuration = factory.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "http", Name: "request_duration_seconds",
		Help:    "HTTP request latency by route.",
		Buckets: prometheus.DefBuckets,
	}, []string{"route"})

	// Build info
	BuildInfo = factory.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Name: "build_info",
		Help: "Build metadata; value is always 1.",
	}, []string{"version", "commit"})
)

func init() {
	Registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
}

// Handler serves the registry in the Prometheus exposition format.
func Handler() http.Handler {
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{Registry: Registry})
}
