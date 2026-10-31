package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	atlasv1 "github.com/manziosee/atlas/api/v1"
	"github.com/manziosee/atlas/internal/routing"
	"github.com/manziosee/atlas/internal/security"
	"github.com/manziosee/atlas/internal/storage"
)

const registryServiceName = "atlas.v1.Registry"

// writeMethods need the write scope on both gRPC services.
var writeMethods = map[string]bool{
	"RegisterService": true, "UpdateService": true, "Heartbeat": true, "DeleteService": true,
}

// NewGRPCServer serves the typed atlas.registry.v1.RegistryService and the
// legacy Struct-based atlas.v1.Registry with the same bearer-token, scope and
// per-client rate checks as the HTTP API.
func NewGRPCServer(deps Deps, tlsConfig *tls.Config) *grpc.Server {
	if deps.Selector == nil {
		deps.Selector = routing.NewSelector()
	}
	options := []grpc.ServerOption{grpc.UnaryInterceptor(grpcAuth(deps)), grpc.StreamInterceptor(grpcStreamAuth(deps))}
	if tlsConfig != nil {
		options = append(options, grpc.Creds(credentials.NewTLS(tlsConfig.Clone())))
	}
	server := grpc.NewServer(options...)
	server.RegisterService(&grpcRegistryServiceDesc, &grpcRegistry{deps: deps})
	atlasv1.RegisterRegistryServiceServer(server, &typedRegistry{deps: deps})
	return server
}

func grpcAuth(deps Deps) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx, done, err := authorizeRPC(ctx, deps, info.FullMethod)
		if err != nil {
			return nil, err
		}
		response, err := handler(ctx, req)
		done(err)
		return response, err
	}
}

func grpcStreamAuth(deps Deps) grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, done, err := authorizeRPC(stream.Context(), deps, info.FullMethod)
		if err != nil {
			return err
		}
		err = handler(srv, &authorizedStream{ServerStream: stream, ctx: ctx})
		done(err)
		return err
	}
}

type authorizedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *authorizedStream) Context() context.Context { return s.ctx }

// authorizeRPC applies rate limiting and scope checks and returns a context
// carrying the credential grant, plus a callback that logs the outcome.
func authorizeRPC(ctx context.Context, deps Deps, method string) (context.Context, func(error), error) {
	remote, forwarded := "unknown", ""
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		remote = p.Addr.String()
	}
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if values := md.Get("x-forwarded-for"); len(values) != 0 {
			forwarded = values[0]
		}
	}
	if deps.RateLimiter != nil && !deps.RateLimiter.AllowClient(remote, forwarded) {
		return ctx, nil, status.Error(codes.ResourceExhausted, "rate limit exceeded")
	}
	token, found := bearerMetadata(ctx)
	if deps.Auth == nil || !found {
		return ctx, nil, status.Error(codes.Unauthenticated, "valid bearer token required")
	}
	scope := security.ScopeRead
	if writeMethods[method[strings.LastIndex(method, "/")+1:]] {
		scope = security.ScopeWrite
	}
	granted, tokenID, ok := deps.Auth.GrantFor(ctx, token, scope)
	if !ok {
		reason, code, message := "unauthenticated", codes.Unauthenticated, "valid bearer token required"
		if deps.Auth.AuthenticateTokenOnly(token) {
			reason, code, message = "insufficient_scope", codes.PermissionDenied, "credential lacks required scope"
		}
		if deps.Log != nil {
			deps.Log.Warn("grpc request denied", "method", method, "reason", reason, "remote", remote)
		}
		return ctx, nil, status.Error(code, message)
	}
	started := time.Now()
	client := remote
	if host, _, err := net.SplitHostPort(remote); err == nil {
		client = host
	}
	return withRequestOrigin(granted, false, client), func(err error) {
		if deps.Log != nil {
			deps.Log.Info("grpc request", "method", method, "status", status.Code(err).String(), "duration_ms", time.Since(started).Milliseconds(), "remote", remote, "token_id", tokenID)
		}
	}, nil
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

// grpcError maps registry operation errors to gRPC status codes.
func grpcError(err error) error {
	if err == nil {
		return nil
	}
	var invalidInput validationError
	var alive ownerAliveError
	var notOwner *storage.NotOwnerError
	var remote *remoteError
	var unreachable *ownerUnreachableError
	switch {
	case errors.As(err, &remote):
		return status.Error(grpcCodeForHTTP(remote.status), remote.body.Code+": "+remote.body.Error)
	case errors.As(err, &unreachable):
		return status.Error(codes.Unavailable, "owner_unreachable: node "+unreachable.owner+" did not answer; retry shortly")
	case errors.As(err, &invalidInput):
		return status.Error(codes.InvalidArgument, invalidInput.msg)
	case errors.Is(err, storage.ErrNotFound):
		return status.Error(codes.NotFound, "service not found")
	case errors.As(err, &alive):
		return status.Error(codes.FailedPrecondition, "owner_alive: node "+alive.owner+" owns this service and is alive; delete through it or use an admin credential")
	case errors.As(err, &notOwner):
		return status.Error(codes.FailedPrecondition, "not_owner: service is owned by node "+notOwner.Owner)
	case errors.Is(err, storage.ErrRevisionMismatch):
		return status.Error(codes.Aborted, "precondition_failed: service revision does not match")
	case errors.Is(err, storage.ErrNoTTL):
		return status.Error(codes.FailedPrecondition, "no_ttl: service was registered without ttl_seconds")
	case errors.Is(err, routing.ErrNoInstance):
		return status.Error(codes.Unavailable, "no healthy service instance available")
	case errors.Is(err, errRegistryUnavailable):
		return status.Error(codes.Unavailable, "service registry unavailable")
	default:
		return status.Error(codes.Internal, "internal error")
	}
}

// grpcCodeForHTTP maps an owner's HTTP status relayed by forwarding.
func grpcCodeForHTTP(status int) codes.Code {
	switch status {
	case 400:
		return codes.InvalidArgument
	case 401:
		return codes.Unauthenticated
	case 403:
		return codes.PermissionDenied
	case 404:
		return codes.NotFound
	case 409, 412:
		return codes.FailedPrecondition
	case 429:
		return codes.ResourceExhausted
	case 502, 503, 504:
		return codes.Unavailable
	default:
		return codes.Internal
	}
}

// --- legacy Struct-based atlas.v1.Registry --------------------------------

type grpcRegistry struct{ deps Deps }

type grpcRegistryServer interface {
	RegisterService(context.Context, *structpb.Struct) (*structpb.Struct, error)
	ListServices(context.Context, *structpb.Struct) (*structpb.Struct, error)
	GetService(context.Context, *structpb.Struct) (*structpb.Struct, error)
	DeleteService(context.Context, *structpb.Struct) (*structpb.Struct, error)
	LookupService(context.Context, *structpb.Struct) (*structpb.Struct, error)
}

func (g *grpcRegistry) RegisterService(ctx context.Context, input *structpb.Struct) (*structpb.Struct, error) {
	body, err := protojson.Marshal(input)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid service request")
	}
	var spec serviceSpec
	if err := json.Unmarshal(body, &spec); err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid service request")
	}
	service, _, err := g.deps.register(ctx, spec, "")
	if err != nil {
		return nil, grpcError(err)
	}
	return toProtoStruct(service)
}

func (g *grpcRegistry) ListServices(ctx context.Context, input *structpb.Struct) (*structpb.Struct, error) {
	values := input.AsMap()
	limit := 0
	if raw, ok := values["limit"].(float64); ok {
		limit = int(raw)
	}
	filter := storage.Filter{
		Name: stringField(values, "name"), Region: stringField(values, "region"), Health: storage.Health(stringField(values, "health")),
		Version: stringField(values, "version"), NodeID: stringField(values, "node_id"),
	}
	page, err := g.deps.listPage(ctx, filter, stringField(values, "cursor"), limit)
	if err != nil {
		return nil, grpcError(err)
	}
	response := map[string]any{"count": len(page.Services), "services": page.Services}
	if page.NextCursor != "" {
		response["next_cursor"] = page.NextCursor
	}
	return toProtoStruct(response)
}

func (g *grpcRegistry) LookupService(ctx context.Context, input *structpb.Struct) (*structpb.Struct, error) {
	values := input.AsMap()
	service, err := g.deps.lookup(ctx, lookupRequest{
		Name: stringField(values, "name"), Region: stringField(values, "region"), Key: stringField(values, "key"),
		Stale: stringField(values, "stale"), Latency: stringField(values, "latency"), Tolerance: stringField(values, "tolerance"),
	})
	if err != nil {
		return nil, grpcError(err)
	}
	return toProtoStruct(service)
}

func (g *grpcRegistry) GetService(ctx context.Context, input *structpb.Struct) (*structpb.Struct, error) {
	if g.deps.Registry == nil {
		return nil, grpcError(errRegistryUnavailable)
	}
	service, err := g.deps.Registry.Get(ctx, stringField(input.AsMap(), "id"))
	if err != nil {
		return nil, grpcError(err)
	}
	return toProtoStruct(service)
}

func (g *grpcRegistry) DeleteService(ctx context.Context, input *structpb.Struct) (*structpb.Struct, error) {
	if err := g.deps.deregister(ctx, stringField(input.AsMap(), "id")); err != nil {
		return nil, grpcError(err)
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
