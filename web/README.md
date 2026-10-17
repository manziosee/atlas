# Atlas dashboard (Phase 5)

Live cluster graph, node health, gossip event stream, service locations and a
latency map. It will be served by `atlas-node` from an embedded build
(`go:embed`) and consume a Server-Sent Events stream fed by
`membership.Memberlist.Events()`.
