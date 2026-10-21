# Atlas membership health

Use this runbook when a membership, probe-timeout, or broadcast-queue alert
fires.

1. Compare `atlas_membership_members` across nodes. A disagreement may be a
   real partition; first check node scrape health and the gossip network.
2. Inspect packet rejection reasons, probe RTT, timeout rate, local health
   score, and queue drops. Clock-skew or decrypt rejects often indicate
   misconfigured keys or clocks.
3. Verify UDP and TCP port 7946 are reachable between peers. Check Kubernetes
   NetworkPolicy, cloud firewall, DNS seeds, and advertised addresses.
4. For a partition, restore connectivity and allow TCP push-pull anti-entropy
   to converge. Avoid manually deleting SQLite tombstones or changing
   incarnation values.
5. Confirm all surviving nodes converge on the same alive-member count and
   that queue drops and probe timeouts return to baseline.

For persistent failure, collect node logs, `/metrics`, advertised addresses,
and a copy of each node's database before making state changes.
