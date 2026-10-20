package api

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/manziosee/atlas/internal/routing"
	"github.com/manziosee/atlas/internal/security"
	"github.com/manziosee/atlas/internal/storage"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

const registryServiceName = "atlas.v1.Registry"

type grpcRegistry struct{ deps Deps }

type grpcRegistryServer interface {
	RegisterService(context.Context, *structpb.Struct) (*structpb.Struct, error)
	ListServices(context.Context, *structpb.Struct) (*structpb.Struct, error)
	GetService(context.Context, *structpb.Struct) (*structpb.Struct, error)
	DeleteService(context.Context, *structpb.Struct) (*structpb.Struct, error)
	LookupService(context.Context, *structpb.Struct) (*structpb.Struct, error)
}

// NewGRPCServer creates the protobuf gRPC service with the same token and
// per-client rate checks as the HTTP control API.
func NewGRPCServer(deps Deps, tlsConfig *tls.Config) *grpc.Server {
	if deps.Selector == nil {
		deps.Selector = routing.NewSelector()
	}
	options := []grpc.ServerOption{grpc.UnaryInterceptor(grpcAuth(deps))}
	if tlsConfig != nil {
		options = append(options, grpc.Creds(credentials.NewTLS(tlsConfig.Clone())))
	}
	server := grpc.NewServer(options...)
	server.RegisterService(&grpcRegistryServiceDesc, &grpcRegistry{deps: deps})
	return server
}

func grpcAuth(deps Deps) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		remote, forwarded := "unknown", ""
		if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
			remote = p.Addr.String()
		}
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			values := md.Get("x-forwarded-for")
			if len(values) != 0 {
				forwarded = values[0]
			}
		}
		if deps.RateLimiter != nil && !deps.RateLimiter.AllowClient(remote, forwarded) {
			return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded")
		}
		token, found := bearerMetadata(ctx)
		if deps.Auth == nil || !found {
			return nil, status.Error(codes.Unauthenticated, "valid bearer token required")
		}
		scope := security.ScopeRead
		if strings.HasSuffix(info.FullMethod, "/RegisterService") || strings.HasSuffix(info.FullMethod, "/DeleteService") {
			scope = security.ScopeWrite
		}
		tokenID, ok := deps.Auth.Authorize(token, scope)
		if !ok {
			if deps.Auth.AuthenticateTokenOnly(token) {
				if deps.Log != nil {
					deps.Log.Warn("grpc request denied", "method", info.FullMethod, "reason", "insufficient_scope", "remote", remote)
				}
				return nil, status.Error(codes.PermissionDenied, "credential lacks required scope")
			}
			if deps.Log != nil {
				deps.Log.Warn("grpc request denied", "method", info.FullMethod, "reason", "unauthenticated", "remote", remote)
			}
			return nil, status.Error(codes.Unauthenticated, "valid bearer token required")
		}
		started := time.Now()
		response, err := handler(ctx, req)
		if deps.Log != nil {
			deps.Log.Info("grpc request", "method", info.FullMethod, "status", status.Code(err).String(), "duration_ms", time.Since(started).Milliseconds(), "remote", remote, "token_id", tokenID)
		}
		return response, err
	}
}

func bearerMetadata(ctx context.Context) (string, bool) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", false
	}
	for _, header := range md.Get("authorization") {
		if len(header) >= len("Bearer ") && strings.EqualFold(header[:len("Bearer ")], "Bearer ") {
			return header[len("Bearer "):], true
		}
	}
	return "", false
}

func (g *grpcRegistry) RegisterService(ctx context.Context, input *structpb.Struct) (*structpb.Struct, error) {
	if g.deps.Registry == nil {
		return nil, status.Error(codes.Unavailable, "service registry unavailable")
	}
	body, err := protojson.Marshal(input)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid service request")
	}
	var request registerServiceRequest
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid service request")
	}
	if err := validateServiceRequest(request); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	var randomID [16]byte
	if _, err := rand.Read(randomID[:]); err != nil {
		return nil, status.Error(codes.Internal, "could not allocate service ID")
	}
	node := g.deps.Cluster.LocalNode()
	service, err := g.deps.Registry.Register(ctx, storage.Service{
		ID: hex.EncodeToString(randomID[:]), Name: request.Name, NodeID: node.ID,
		Address: request.Address, Region: node.Region, Version: request.Version,
		Tags: request.Tags, HealthCheck: request.HealthCheck, Health: storage.HealthUnknown,
	})
	if err != nil {
		return nil, status.Error(codes.Internal, "could not register service")
	}
	return toProtoStruct(service)
}

func (g *grpcRegistry) ListServices(ctx context.Context, input *structpb.Struct) (*structpb.Struct, error) {
	if g.deps.Registry == nil {
		return nil, status.Error(codes.Unavailable, "service registry unavailable")
	}
	values := input.AsMap()
	filter := storage.Filter{Name: stringField(values, "name"), Region: stringField(values, "region"), Health: storage.Health(stringField(values, "health"))}
	if filter.Health != "" && filter.Health != storage.HealthHealthy && filter.Health != storage.HealthUnhealthy && filter.Health != storage.HealthUnknown {
		return nil, status.Error(codes.InvalidArgument, "invalid health filter")
	}
	services, err := g.deps.Registry.List(ctx, filter)
	if err != nil {
		return nil, status.Error(codes.Internal, "could not list services")
	}
	return toProtoStruct(map[string]any{"count": len(services), "services": services})
}

func (g *grpcRegistry) LookupService(ctx context.Context, input *structpb.Struct) (*structpb.Struct, error) {
	if g.deps.Registry == nil || g.deps.Selector == nil {
		return nil, status.Error(codes.Unavailable, "service registry unavailable")
	}
	values := input.AsMap()
	name, region, key := stringField(values, "name"), stringField(values, "region"), stringField(values, "key")
	if name == "" || len(name) > 128 || len(region) > 64 || len(key) > 256 {
		return nil, status.Error(codes.InvalidArgument, "invalid lookup parameters")
	}
	services, err := g.deps.Registry.List(ctx, storage.Filter{Name: name})
	if err != nil {
		return nil, status.Error(codes.Internal, "could not look up service")
	}
	service, err := g.deps.Selector.SelectWithLatency(services, region, key, g.deps.nodeLatency)
	if errors.Is(err, routing.ErrNoInstance) {
		return nil, status.Error(codes.Unavailable, "no healthy service instance available")
	}
	if err != nil {
		return nil, status.Error(codes.Internal, "could not select service instance")
	}
	return toProtoStruct(service)
}

func (g *grpcRegistry) GetService(ctx context.Context, input *structpb.Struct) (*structpb.Struct, error) {
	if g.deps.Registry == nil {
		return nil, status.Error(codes.Unavailable, "service registry unavailable")
	}
	service, err := g.deps.Registry.Get(ctx, stringField(input.AsMap(), "id"))
	if errors.Is(err, storage.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "service not found")
	}
	if err != nil {
		return nil, status.Error(codes.Internal, "could not read service")
	}
	return toProtoStruct(service)
}

func (g *grpcRegistry) DeleteService(ctx context.Context, input *structpb.Struct) (*structpb.Struct, error) {
	if g.deps.Registry == nil {
		return nil, status.Error(codes.Unavailable, "service registry unavailable")
	}
	if err := g.deps.Registry.Delete(ctx, stringField(input.AsMap(), "id"), g.deps.Cluster.LocalNode().ID); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "service not found")
		}
		return nil, status.Error(codes.PermissionDenied, "service cannot be removed by this node")
	}
	return structpb.NewStruct(map[string]any{"deleted": true})
}

func stringField(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}

func toProtoStruct(value any) (*structpb.Struct, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, status.Error(codes.Internal, "could not encode response")
	}
	result := &structpb.Struct{}
	if err := protojson.Unmarshal(body, result); err != nil {
		return nil, status.Error(codes.Internal, "could not encode response")
	}
	return result, nil
}

func registryMethod(method string, call func(*grpcRegistry, context.Context, *structpb.Struct) (*structpb.Struct, error)) grpc.MethodHandler {
	return func(server any, ctx context.Context, decode func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
		input := new(structpb.Struct)
		if err := decode(input); err != nil {
			return nil, err
		}
		invoke := func(ctx context.Context, req any) (any, error) {
			return call(server.(*grpcRegistry), ctx, req.(*structpb.Struct))
		}
		if interceptor == nil {
			return invoke(ctx, input)
		}
		info := &grpc.UnaryServerInfo{Server: server, FullMethod: "/" + registryServiceName + "/" + method}
		return interceptor(ctx, input, info, invoke)
	}
}

var grpcRegistryServiceDesc = grpc.ServiceDesc{
	ServiceName: registryServiceName,
	HandlerType: (*grpcRegistryServer)(nil),
	Methods: []grpc.MethodDesc{
		{MethodName: "RegisterService", Handler: registryMethod("RegisterService", (*grpcRegistry).RegisterService)},
		{MethodName: "ListServices", Handler: registryMethod("ListServices", (*grpcRegistry).ListServices)},
		{MethodName: "GetService", Handler: registryMethod("GetService", (*grpcRegistry).GetService)},
		{MethodName: "DeleteService", Handler: registryMethod("DeleteService", (*grpcRegistry).DeleteService)},
		{MethodName: "LookupService", Handler: registryMethod("LookupService", (*grpcRegistry).LookupService)},
	},
	Metadata: "api/v1/registry.proto",
}
