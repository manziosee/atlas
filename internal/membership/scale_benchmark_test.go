package membership

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// BenchmarkClusterConvergence measures membership anti-entropy across lossless
// in-process nodes at sizes representative of the scale goal. Run explicitly:
// go test -run '^$' -bench '^BenchmarkClusterConvergence$' -benchtime=1x -timeout=10m ./internal/membership
func BenchmarkClusterConvergence(b *testing.B) {
	for _, size := range []int{100, 500, 1000} {
		b.Run(fmt.Sprintf("nodes-%d", size), func(b *testing.B) {
			var total time.Duration
			for range b.N {
				total += simulateConvergence(b, size)
			}
			b.ReportMetric(float64(total)/float64(b.N)/float64(time.Millisecond), "convergence-ms")
			b.ReportMetric(float64(size), "nodes")
		})
	}
}

func simulateConvergence(b *testing.B, size int) time.Duration {
	b.Helper()
	fn := newFakeNet()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	nodes := make([]*Memberlist, size)
	const basePort = 10000
	for i := range size {
		addr := fmt.Sprintf("127.0.0.1:%d", basePort+i)
		cfg := testConfig(fmt.Sprintf("sim-%04d", i), addr)
		nodes[i] = New(cfg, fn.attach(addr), logger)
		wg.Go(func() { nodes[i].receiveLoop(ctx) })
	}
	for _, node := range nodes[1:] {
		if _, err := node.Join(ctx, []string{nodes[0].LocalNode().Addr}); err != nil {
			b.Fatalf("join scale simulation: %v", err)
		}
	}
	started := time.Now()
	for round := 0; ; round++ {
		if simulatedClusterConverged(nodes, size) {
			return time.Since(started)
		}
		if round >= 600 {
			b.Fatalf("%d-node cluster did not converge in %d gossip rounds (%s)", size, round, simulatedMemberSummary(nodes))
		}
		if round%antiEntropyEvery == antiEntropyEvery-1 {
			for _, node := range nodes {
				if peer, ok := node.randomMember(func(member *Member) bool { return member.State == StateAlive }); ok {
					_ = node.sendFullState(context.Background(), peer.Addr, msgPushPullRequest, nil, false)
				}
			}
		} else {
			for _, node := range nodes {
				for _, peer := range node.randomMembers(3, func(member *Member) bool { return member.State == StateAlive }) {
					_ = node.send(context.Background(), peer.Addr, msgSync, 0, node.sample(maxPiggyback, peer.ID))
				}
			}
		}
		select {
		case <-ctx.Done():
			b.Fatalf("%d-node cluster exceeded the two-minute simulation budget (%s)", size, simulatedMemberSummary(nodes))
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func simulatedClusterConverged(nodes []*Memberlist, size int) bool {
	for _, node := range nodes {
		node.mu.RLock()
		alive := 1 // local node is always alive
		for _, member := range node.members {
			if member.State == StateAlive {
				alive++
			}
		}
		node.mu.RUnlock()
		if alive != size {
			return false
		}
	}
	return true
}

func simulatedMemberSummary(nodes []*Memberlist) string {
	minimum, maximum, complete := len(nodes), 0, 0
	for _, node := range nodes {
		node.mu.RLock()
		alive := 1
		for _, member := range node.members {
			if member.State == StateAlive {
				alive++
			}
		}
		node.mu.RUnlock()
		minimum = min(minimum, alive)
		maximum = max(maximum, alive)
		if alive == len(nodes) {
			complete++
		}
	}
	return fmt.Sprintf("alive views min=%d max=%d, fully converged nodes=%d/%d", minimum, maximum, complete, len(nodes))
}
