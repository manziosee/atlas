package api

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/manziosee/atlas/internal/security"
	"github.com/manziosee/atlas/internal/storage"
)

func TestGRPCRegistryAuthAndLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "registry.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := NewGRPCServer(Deps{
		Cluster: fakeCluster{}, Registry: store,
		Auth:        security.NewTokenAuthenticator([]string{token}),
		RateLimiter: security.NewRateLimiter(1000, 1000),
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

	var response structpb.Struct
	if err := conn.Invoke(ctx, "/atlas.v1.Registry/ListServices", &structpb.Struct{}, &response); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated ListServices error = %v, want Unauthenticated", err)
	}
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	registration, err := structpb.NewStruct(map[string]any{
		"name": "grpc-service", "address": "127.0.0.1:9191", "version": "v1",
		"tags": []any{"blue", "grpc"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Invoke(ctx, "/atlas.v1.Registry/RegisterService", registration, &response); err != nil {
		t.Fatal(err)
	}
	id := response.AsMap()["id"].(string)
	if id == "" {
		t.Fatal("RegisterService returned an empty ID")
	}

	query, _ := structpb.NewStruct(map[string]any{"name": "grpc-service", "health": "unknown"})
	if err := conn.Invoke(ctx, "/atlas.v1.Registry/ListServices", query, &response); err != nil {
		t.Fatal(err)
	}
	if response.AsMap()["count"] != float64(1) {
		t.Fatalf("ListServices response = %v", response.AsMap())
	}
	lookup, _ := structpb.NewStruct(map[string]any{"name": "grpc-service", "region": "jnb", "key": "customer-1"})
	if err := conn.Invoke(ctx, "/atlas.v1.Registry/LookupService", lookup, &response); err != nil {
		t.Fatal(err)
	}
	if response.AsMap()["id"] != id {
		t.Fatalf("LookupService response = %v", response.AsMap())
	}
	get, _ := structpb.NewStruct(map[string]any{"id": id})
	if err := conn.Invoke(ctx, "/atlas.v1.Registry/GetService", get, &response); err != nil {
		t.Fatal(err)
	}
	if response.AsMap()["id"] != id {
		t.Fatalf("GetService response = %v", response.AsMap())
	}
	if err := conn.Invoke(ctx, "/atlas.v1.Registry/DeleteService", get, &response); err != nil {
		t.Fatal(err)
	}
	if response.AsMap()["deleted"] != true {
		t.Fatalf("DeleteService response = %v", response.AsMap())
	}
}

func TestGRPCScopesGuardMutations(t *testing.T) {
	tokenRead := "rrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrr"
	auth, err := security.NewCredentialAuthenticator([]security.Credential{{Token: tokenRead, Scopes: []security.Scope{security.ScopeRead}, ExpiresAt: time.Now().Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	interceptor := grpcAuth(Deps{Auth: auth})
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+tokenRead))
	handler := func(context.Context, any) (any, error) { return "ok", nil }
	_, err = interceptor(ctx, &structpb.Struct{}, &grpc.UnaryServerInfo{FullMethod: "/atlas.v1.Registry/ListServices"}, handler)
	if err != nil {
		t.Fatalf("read-scoped ListServices error = %v", err)
	}
	_, err = interceptor(ctx, &structpb.Struct{}, &grpc.UnaryServerInfo{FullMethod: "/atlas.v1.Registry/RegisterService"}, handler)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("read-scoped RegisterService error = %v, want PermissionDenied", err)
	}
}
