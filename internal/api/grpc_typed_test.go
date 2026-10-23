package api

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	atlasv1 "github.com/manziosee/atlas/api/v1"
	"github.com/manziosee/atlas/internal/events"
	"github.com/manziosee/atlas/internal/membership"
)

func typedClient(t *testing.T, h *harness) atlasv1.RegistryServiceClient {
	t.Helper()
	server := NewGRPCServer(Deps{
		Cluster: h.cluster, Registry: h.store, Events: h.broker,
		Auth: h.auth,
	}, nil)
	listener := bufconn.Listen(1 << 20)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return atlasv1.NewRegistryServiceClient(conn)
}

func withToken(ctx context.Context, token string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
}

func TestTypedGRPCRegistryLifecycle(t *testing.T) {
	h := newHarness(t, nil)
	h.cluster.peers = []membership.Member{{ID: "n2", Addr: "10.0.0.2:7946", State: membership.StateAlive}}
	client := typedClient(t, h)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	write, read := withToken(ctx, writeToken), withToken(ctx, readToken)

	if _, err := client.GetNode(ctx, &atlasv1.GetNodeRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("anonymous GetNode = %v", err)
	}
	spec := &atlasv1.ServiceSpec{Name: "typed", Address: "10.0.5.1:443", Tags: []string{"grpc"}, TtlSeconds: 30}
	if _, err := client.RegisterService(read, &atlasv1.RegisterServiceRequest{Spec: spec}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("read-scoped register = %v", err)
	}
	first, err := client.RegisterService(write, &atlasv1.RegisterServiceRequest{Spec: spec, IdempotencyKey: "k1"})
	if err != nil || !first.GetCreated() || first.GetService().GetTtlSeconds() != 30 {
		t.Fatalf("register = %v %v", first, err)
	}
	retry, err := client.RegisterService(write, &atlasv1.RegisterServiceRequest{Spec: spec, IdempotencyKey: "k1"})
	if err != nil || retry.GetCreated() || retry.GetService().GetId() != first.GetService().GetId() {
		t.Fatalf("idempotent retry = %v %v", retry, err)
	}
	if _, err := client.RegisterService(write, &atlasv1.RegisterServiceRequest{Spec: &atlasv1.ServiceSpec{Name: "bad name", Address: "x"}}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid register = %v", err)
	}
	id := first.GetService().GetId()

	beat, err := client.Heartbeat(write, &atlasv1.HeartbeatRequest{Id: id})
	if err != nil || beat.GetHealth() != atlasv1.Health_HEALTH_HEALTHY || beat.GetHeartbeatAt() == nil {
		t.Fatalf("heartbeat = %v %v", beat, err)
	}
	mask := &fieldmaskpb.FieldMask{Paths: []string{"version"}}
	updated, err := client.UpdateService(write, &atlasv1.UpdateServiceRequest{Id: id, Spec: &atlasv1.ServiceSpec{Version: "v2"}, UpdateMask: mask, IfRevision: beat.GetRevision()})
	if err != nil || updated.GetVersion() != "v2" || updated.GetAddress() != "10.0.5.1:443" || updated.GetHealth() != atlasv1.Health_HEALTH_HEALTHY {
		t.Fatalf("masked update = %v %v", updated, err)
	}
	if _, err := client.UpdateService(write, &atlasv1.UpdateServiceRequest{Id: id, Spec: spec, IfRevision: 1}); status.Code(err) != codes.Aborted {
		t.Fatalf("stale update = %v", err)
	}
	if _, err := client.UpdateService(write, &atlasv1.UpdateServiceRequest{Id: id, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"node_id"}}}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("non-updatable path = %v", err)
	}

	for i := range 3 {
		if _, err := client.RegisterService(write, &atlasv1.RegisterServiceRequest{Spec: &atlasv1.ServiceSpec{Name: "typed", Address: "10.0.5." + string(rune('2'+i)) + ":443"}}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := client.ListServices(read, &atlasv1.ListServicesRequest{Name: "typed", PageSize: 3})
	if err != nil || len(page.GetServices()) != 3 || page.GetNextPageToken() == "" {
		t.Fatalf("first page = %d %q %v", len(page.GetServices()), page.GetNextPageToken(), err)
	}
	rest, err := client.ListServices(read, &atlasv1.ListServicesRequest{Name: "typed", PageSize: 3, PageToken: page.GetNextPageToken()})
	if err != nil || len(rest.GetServices()) != 1 || rest.GetNextPageToken() != "" {
		t.Fatalf("last page = %d %v", len(rest.GetServices()), err)
	}
	tagged, err := client.ListServices(read, &atlasv1.ListServicesRequest{Tags: []string{"grpc"}, Health: atlasv1.Health_HEALTH_HEALTHY})
	if err != nil || len(tagged.GetServices()) != 1 {
		t.Fatalf("filtered list = %v %v", tagged, err)
	}
	selected, err := client.LookupService(read, &atlasv1.LookupServiceRequest{Name: "typed", Tags: []string{"grpc"}})
	if err != nil || selected.GetId() != id {
		t.Fatalf("lookup = %v %v", selected, err)
	}

	node, err := client.GetNode(read, &atlasv1.GetNodeRequest{})
	if err != nil || node.GetNode().GetId() != "n1" || node.GetNode().GetState() != atlasv1.MemberState_MEMBER_STATE_ALIVE {
		t.Fatalf("node = %v %v", node, err)
	}
	members, err := client.ListMembers(read, &atlasv1.ListMembersRequest{})
	if err != nil || len(members.GetMembers()) != 2 {
		t.Fatalf("members = %v %v", members, err)
	}
	if _, err := client.DeleteService(write, &atlasv1.DeleteServiceRequest{Id: id}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetService(read, &atlasv1.GetServiceRequest{Id: id}); status.Code(err) != codes.NotFound {
		t.Fatalf("get after delete = %v", err)
	}
}

func TestTypedGRPCWatchStreamsAndResumes(t *testing.T) {
	h := newHarness(t, nil)
	client := typedClient(t, h)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	read := withToken(ctx, readToken)

	stream, err := client.Watch(read, &atlasv1.WatchRequest{Kinds: []string{events.KindService}, Service: "watched"})
	if err != nil {
		t.Fatal(err)
	}
	// The server subscribes asynchronously; wait so no event is missed.
	waitSubscribed(t, h.broker)
	h.register(`{"name":"other","address":"10.0.6.1:80"}`)
	h.register(`{"name":"watched","address":"10.0.6.2:80"}`)
	event, err := stream.Recv()
	if err != nil || event.GetType() != events.ServiceRegistered || event.GetService().GetName() != "watched" {
		t.Fatalf("watch event = %v %v", event, err)
	}

	resumed, err := client.Watch(read, &atlasv1.WatchRequest{LastEventId: "elsewhere.1"})
	if err != nil {
		t.Fatal(err)
	}
	if reset, err := resumed.Recv(); err != nil || reset.GetType() != "reset" {
		t.Fatalf("resume from unknown ID = %v %v", reset, err)
	}
	bad, err := client.Watch(read, &atlasv1.WatchRequest{Kinds: []string{"bogus"}})
	if err == nil {
		_, err = bad.Recv()
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad kinds = %v", err)
	}
}

// waitSubscribed blocks until a subscriber is attached to broker.
func waitSubscribed(t *testing.T, broker *events.Broker) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if broker.Subscribers() > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no subscriber attached")
}
