package api

import (
	"context"
	"slices"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	atlasv1 "github.com/manziosee/atlas/api/v1"
	"github.com/manziosee/atlas/internal/events"
	"github.com/manziosee/atlas/internal/membership"
	"github.com/manziosee/atlas/internal/storage"
	"github.com/manziosee/atlas/internal/version"
)

// typedRegistry implements atlas.registry.v1.RegistryService.
type typedRegistry struct {
	atlasv1.UnimplementedRegistryServiceServer
	deps Deps
}

var updatablePaths = []string{
	"name", "address", "version", "tags", "health_check", "ttl_seconds",
	"check_interval_seconds", "unhealthy_threshold", "healthy_threshold", "draining",
}

func (t *typedRegistry) RegisterService(ctx context.Context, req *atlasv1.RegisterServiceRequest) (*atlasv1.RegisterServiceResponse, error) {
	spec := specFromProto(req.GetSpec())
	spec.ID = req.GetId()
	service, outcome, err := t.deps.register(ctx, spec, req.GetIdempotencyKey())
	if err != nil {
		return nil, grpcError(err)
	}
	return &atlasv1.RegisterServiceResponse{Service: serviceToProto(service), Created: outcome == storage.Created}, nil
}

func (t *typedRegistry) UpdateService(ctx context.Context, req *atlasv1.UpdateServiceRequest) (*atlasv1.Service, error) {
	paths := req.GetUpdateMask().GetPaths()
	if len(paths) == 0 {
		paths = updatablePaths
	}
	for _, path := range paths {
		if !slices.Contains(updatablePaths, path) {
			return nil, status.Errorf(codes.InvalidArgument, "update_mask path %q is not updatable", path)
		}
	}
	// The mask selects fields from a full patch of the incoming spec.
	full := fullPatch(specFromProto(req.GetSpec()))
	var patch servicePatch
	for _, path := range paths {
		switch path {
		case "name":
			patch.Name = full.Name
		case "address":
			patch.Address = full.Address
		case "version":
			patch.Version = full.Version
		case "tags":
			patch.Tags = full.Tags
		case "health_check":
			patch.HealthCheck = full.HealthCheck
		case "ttl_seconds":
			patch.TTLSeconds = full.TTLSeconds
		case "check_interval_seconds":
			patch.CheckIntervalSeconds = full.CheckIntervalSeconds
		case "unhealthy_threshold":
			patch.UnhealthyThreshold = full.UnhealthyThreshold
		case "healthy_threshold":
			patch.HealthyThreshold = full.HealthyThreshold
		case "draining":
			patch.Draining = full.Draining
		}
	}
	service, err := t.deps.update(ctx, req.GetId(), req.GetIfRevision(), patch)
	if err != nil {
		return nil, grpcError(err)
	}
	return serviceToProto(service), nil
}

func (t *typedRegistry) Heartbeat(ctx context.Context, req *atlasv1.HeartbeatRequest) (*atlasv1.Service, error) {
	service, err := t.deps.heartbeatService(ctx, req.GetId())
	if err != nil {
		return nil, grpcError(err)
	}
	return serviceToProto(service), nil
}

func (t *typedRegistry) GetService(ctx context.Context, req *atlasv1.GetServiceRequest) (*atlasv1.Service, error) {
	if t.deps.Registry == nil {
		return nil, grpcError(errRegistryUnavailable)
	}
	service, err := t.deps.Registry.Get(ctx, req.GetId())
	if err != nil {
		return nil, grpcError(err)
	}
	return serviceToProto(service), nil
}

func (t *typedRegistry) ListServices(ctx context.Context, req *atlasv1.ListServicesRequest) (*atlasv1.ListServicesResponse, error) {
	filter := storage.Filter{
		Name: req.GetName(), Region: req.GetRegion(), Health: healthFromProto(req.GetHealth()),
		Version: req.GetVersion(), NodeID: req.GetNodeId(), Tags: req.GetTags(),
	}
	page, err := t.deps.listPage(ctx, filter, req.GetPageToken(), int(req.GetPageSize()))
	if err != nil {
		return nil, grpcError(err)
	}
	response := &atlasv1.ListServicesResponse{NextPageToken: page.NextCursor, Services: make([]*atlasv1.Service, 0, len(page.Services))}
	for _, service := range page.Services {
		response.Services = append(response.Services, serviceToProto(service))
	}
	return response, nil
}

func (t *typedRegistry) DeleteService(ctx context.Context, req *atlasv1.DeleteServiceRequest) (*atlasv1.DeleteServiceResponse, error) {
	if err := t.deps.deregister(ctx, req.GetId()); err != nil {
		return nil, grpcError(err)
	}
	return &atlasv1.DeleteServiceResponse{}, nil
}

func (t *typedRegistry) LookupService(ctx context.Context, req *atlasv1.LookupServiceRequest) (*atlasv1.Service, error) {
	service, err := t.deps.lookup(ctx, lookupRequest{
		Name: req.GetName(), Region: req.GetRegion(), Key: req.GetKey(), Tags: req.GetTags(),
		Stale: req.GetStalePolicy(), Latency: req.GetLatency(), Tolerance: req.GetLatencyTolerance(),
	})
	if err != nil {
		return nil, grpcError(err)
	}
	return serviceToProto(service), nil
}

func (t *typedRegistry) GetServiceHistory(ctx context.Context, req *atlasv1.GetServiceHistoryRequest) (*atlasv1.GetServiceHistoryResponse, error) {
	entries, next, err := t.deps.history(ctx, storage.HistoryFilter{
		ServiceID: req.GetServiceId(), Name: req.GetName(), Limit: int(req.GetPageSize()), Before: req.GetBefore(),
	})
	if err != nil {
		return nil, grpcError(err)
	}
	response := &atlasv1.GetServiceHistoryResponse{NextBefore: next, Entries: make([]*atlasv1.HistoryEntry, 0, len(entries))}
	for _, entry := range entries {
		response.Entries = append(response.Entries, &atlasv1.HistoryEntry{
			Seq: entry.Seq, ServiceId: entry.ServiceID, Name: entry.Name, Revision: entry.Revision, Change: entry.Change,
			At: timestamp(entry.At), Service: serviceToProto(entry.Service), Deleted: entry.Service.Deleted,
		})
	}
	return response, nil
}

func (t *typedRegistry) GetNode(context.Context, *atlasv1.GetNodeRequest) (*atlasv1.GetNodeResponse, error) {
	return &atlasv1.GetNodeResponse{Node: memberToProto(t.deps.Cluster.LocalNode()), Version: version.Version, Commit: version.Commit}, nil
}

func (t *typedRegistry) ListMembers(context.Context, *atlasv1.ListMembersRequest) (*atlasv1.ListMembersResponse, error) {
	members := t.deps.Cluster.Members()
	response := &atlasv1.ListMembersResponse{Members: make([]*atlasv1.Member, 0, len(members))}
	for _, member := range members {
		response.Members = append(response.Members, memberToProto(member))
	}
	return response, nil
}

func (t *typedRegistry) Watch(req *atlasv1.WatchRequest, stream atlasv1.RegistryService_WatchServer) error {
	if t.deps.Events == nil {
		return status.Error(codes.Unimplemented, "event stream unavailable")
	}
	kinds := map[string]bool{}
	for _, kind := range req.GetKinds() {
		if kind != events.KindMember && kind != events.KindService {
			return status.Error(codes.InvalidArgument, "kinds must contain member and/or service")
		}
		kinds[kind] = true
	}
	if len(kinds) == 0 {
		kinds[events.KindMember], kinds[events.KindService] = true, true
	}
	if len(req.GetService()) > 128 || len(req.GetLastEventId()) > 64 {
		return status.Error(codes.InvalidArgument, "watch parameters exceed limits")
	}
	sub, backlog, reset := t.deps.Events.Subscribe(req.GetLastEventId(), 256)
	defer sub.Close()
	if reset {
		if err := stream.Send(&atlasv1.Event{Type: "reset", At: timestamppb.Now()}); err != nil {
			return err
		}
	}
	send := func(event events.Event) error {
		if !kinds[event.Kind] || (req.GetService() != "" && event.Kind == events.KindService && event.Service.Name != req.GetService()) {
			return nil
		}
		return stream.Send(eventToProto(event))
	}
	for _, event := range backlog {
		if err := send(event); err != nil {
			return err
		}
	}
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case event, ok := <-sub.C:
			if !ok {
				return status.Error(codes.Unavailable, "watcher fell behind; resume with last_event_id")
			}
			if err := send(event); err != nil {
				return err
			}
		}
	}
}

func specFromProto(spec *atlasv1.ServiceSpec) serviceSpec {
	return serviceSpec{
		Name: spec.GetName(), Address: spec.GetAddress(), Version: spec.GetVersion(), Tags: spec.GetTags(),
		HealthCheck: spec.GetHealthCheck(), TTLSeconds: spec.GetTtlSeconds(), CheckIntervalSeconds: spec.GetCheckIntervalSeconds(),
		UnhealthyThreshold: int(spec.GetUnhealthyThreshold()), HealthyThreshold: int(spec.GetHealthyThreshold()), Draining: spec.GetDraining(),
	}
}

func serviceToProto(service storage.Service) *atlasv1.Service {
	return &atlasv1.Service{
		Id: service.ID, Name: service.Name, NodeId: service.NodeID, Address: service.Address, Region: service.Region,
		Version: service.Version, Tags: service.Tags, HealthCheck: service.HealthCheck, Health: healthToProto(service.Health),
		Revision: service.Revision, TtlSeconds: service.TTLSeconds, UpdatedAt: timestamp(service.UpdatedAt),
		CheckedAt: timestamp(service.CheckedAt), HeartbeatAt: timestamp(service.HeartbeatAt),
		CheckIntervalSeconds: service.CheckIntervalSeconds, Draining: service.Draining,
		UnhealthyThreshold: int32(service.UnhealthyThreshold), HealthyThreshold: int32(service.HealthyThreshold), // #nosec G115 -- validated to at most 10.
	}
}

func memberToProto(member membership.Member) *atlasv1.Member {
	state := atlasv1.MemberState_MEMBER_STATE_UNSPECIFIED
	switch member.State {
	case membership.StateAlive:
		state = atlasv1.MemberState_MEMBER_STATE_ALIVE
	case membership.StateSuspect:
		state = atlasv1.MemberState_MEMBER_STATE_SUSPECT
	case membership.StateDead:
		state = atlasv1.MemberState_MEMBER_STATE_DEAD
	case membership.StateLeft:
		state = atlasv1.MemberState_MEMBER_STATE_LEFT
	}
	return &atlasv1.Member{Id: member.ID, Addr: member.Addr, Region: member.Region, State: state, Incarnation: member.Incarnation, Since: timestamp(member.Since), ApiAddr: member.APIAddr}
}

func eventToProto(event events.Event) *atlasv1.Event {
	out := &atlasv1.Event{Id: event.ID, Type: event.Type, Kind: event.Kind, At: timestamp(event.At)}
	switch {
	case event.Member != nil:
		out.Subject = &atlasv1.Event_Member{Member: memberToProto(*event.Member)}
	case event.Service != nil:
		out.Subject = &atlasv1.Event_Service{Service: serviceToProto(*event.Service)}
	}
	return out
}

func healthToProto(health storage.Health) atlasv1.Health {
	switch health {
	case storage.HealthHealthy:
		return atlasv1.Health_HEALTH_HEALTHY
	case storage.HealthUnhealthy:
		return atlasv1.Health_HEALTH_UNHEALTHY
	case storage.HealthUnknown:
		return atlasv1.Health_HEALTH_UNKNOWN
	default:
		return atlasv1.Health_HEALTH_UNSPECIFIED
	}
}

func healthFromProto(health atlasv1.Health) storage.Health {
	switch health {
	case atlasv1.Health_HEALTH_HEALTHY:
		return storage.HealthHealthy
	case atlasv1.Health_HEALTH_UNHEALTHY:
		return storage.HealthUnhealthy
	case atlasv1.Health_HEALTH_UNKNOWN:
		return storage.HealthUnknown
	default:
		return ""
	}
}

func timestamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}
