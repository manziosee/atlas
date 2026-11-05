package provider

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/manziosee/atlas/pkg/atlas"
)

// base is shared by every data source: it receives the configured client.
type base struct{ client *atlas.Client }

func (b *base) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*atlas.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("got %T", req.ProviderData))
		return
	}
	b.client = client
}

func (b *base) ready(resp *datasource.ReadResponse) bool {
	if b.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Set the Atlas provider's address and token, or ATLAS_ADDR and ATLAS_TOKEN.")
		return false
	}
	return true
}

// instance is a read-only service instance in data sources.
type instance struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Address     types.String `tfsdk:"address"`
	NodeID      types.String `tfsdk:"node_id"`
	Region      types.String `tfsdk:"region"`
	Version     types.String `tfsdk:"version"`
	Tags        []string     `tfsdk:"tags"`
	Health      types.String `tfsdk:"health"`
	HealthCheck types.String `tfsdk:"health_check"`
	TTLSeconds  types.Int64  `tfsdk:"ttl_seconds"`
	Draining    types.Bool   `tfsdk:"draining"`
	Revision    types.Int64  `tfsdk:"revision"`
}

func instanceAttributes() map[string]schema.Attribute {
	s := func(d string) schema.StringAttribute { return schema.StringAttribute{Computed: true, Description: d} }
	return map[string]schema.Attribute{
		"id": s("Instance ID."), "name": s("Service name."), "address": s("host:port."), "node_id": s("Owning node."),
		"region": s("Region."), "version": s("Version."), "health": s("unknown, healthy or unhealthy."),
		"health_check": s("Health-check URL, if any."),
		"tags":         schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "Tags."},
		"ttl_seconds":  schema.Int64Attribute{Computed: true, Description: "Heartbeat TTL, if any."},
		"draining":     schema.BoolAttribute{Computed: true, Description: "Whether it is draining."},
		"revision":     schema.Int64Attribute{Computed: true, Description: "Revision."},
	}
}

func toInstance(s atlas.Service) instance {
	tags := append([]string{}, s.Tags...)
	return instance{
		ID: types.StringValue(s.ID), Name: types.StringValue(s.Name), Address: types.StringValue(s.Address),
		NodeID: types.StringValue(s.NodeID), Region: types.StringValue(s.Region), Version: types.StringValue(s.Version),
		Tags: tags, Health: types.StringValue(string(s.Health)), HealthCheck: types.StringValue(s.HealthCheck),
		TTLSeconds: types.Int64Value(s.TTLSeconds), Draining: types.BoolValue(s.Draining),
		Revision: types.Int64Value(int64(s.Revision)), // #nosec G115 -- revisions stay far below 2^63.
	}
}

// --- atlas_service ---------------------------------------------------------

type serviceDataSource struct{ base }

func newServiceDataSource() datasource.DataSource { return &serviceDataSource{} }

func (d *serviceDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service"
}

func (d *serviceDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := instanceAttributes()
	attrs["id"] = schema.StringAttribute{Required: true, Description: "Instance ID to read."}
	resp.Schema = schema.Schema{Description: "One service instance by ID.", Attributes: attrs}
}

func (d *serviceDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg instance
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || !d.ready(resp) {
		return
	}
	s, err := d.client.Get(ctx, cfg.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Reading the service failed", describe(err))
		return
	}
	out := toInstance(s)
	resp.Diagnostics.Append(resp.State.Set(ctx, &out)...)
}

// --- atlas_services --------------------------------------------------------

type servicesDataSource struct{ base }

type servicesModel struct {
	Name      types.String `tfsdk:"name"`
	Region    types.String `tfsdk:"region"`
	Health    types.String `tfsdk:"health"`
	Version   types.String `tfsdk:"version"`
	NodeID    types.String `tfsdk:"node_id"`
	Tags      []string     `tfsdk:"tags"`
	Instances []instance   `tfsdk:"instances"`
	Addresses []string     `tfsdk:"addresses"`
}

func newServicesDataSource() datasource.DataSource { return &servicesDataSource{} }

func (d *servicesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_services"
}

func (d *servicesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	opt := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Optional: true, Description: desc}
	}
	resp.Schema = schema.Schema{
		Description: "Every service instance matching the filters, for example to feed a load balancer or firewall rule.",
		Attributes: map[string]schema.Attribute{
			"name": opt("Service name."), "region": opt("Region."), "health": opt("unknown, healthy or unhealthy."),
			"version": opt("Version."), "node_id": opt("Owning node."),
			"tags":      schema.ListAttribute{Optional: true, ElementType: types.StringType, Description: "Instances must carry every tag."},
			"addresses": schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "Addresses of the matching instances, sorted."},
			"instances": schema.ListNestedAttribute{Computed: true, Description: "Matching instances.", NestedObject: schema.NestedAttributeObject{Attributes: instanceAttributes()}},
		},
	}
}

func (d *servicesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m servicesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !d.ready(resp) {
		return
	}
	list, err := d.client.List(ctx, atlas.Filter{
		Name: m.Name.ValueString(), Region: m.Region.ValueString(), Health: atlas.Health(m.Health.ValueString()),
		Version: m.Version.ValueString(), NodeID: m.NodeID.ValueString(), Tags: m.Tags,
	})
	if err != nil {
		resp.Diagnostics.AddError("Listing services failed", describe(err))
		return
	}
	m.Instances = []instance{}
	m.Addresses = []string{}
	for _, s := range list {
		m.Instances = append(m.Instances, toInstance(s))
		m.Addresses = append(m.Addresses, s.Address)
	}
	sort.Strings(m.Addresses)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// --- atlas_lookup ----------------------------------------------------------

type lookupDataSource struct{ base }

type lookupModel struct {
	Name      types.String `tfsdk:"name"`
	Region    types.String `tfsdk:"region"`
	Key       types.String `tfsdk:"key"`
	Tags      []string     `tfsdk:"tags"`
	Stale     types.String `tfsdk:"stale"`
	Latency   types.String `tfsdk:"latency"`
	Tolerance types.String `tfsdk:"tolerance"`
	Instance  *instance    `tfsdk:"instance"`
	Address   types.String `tfsdk:"address"`
}

func newLookupDataSource() datasource.DataSource { return &lookupDataSource{} }

func (d *lookupDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_lookup"
}

func (d *lookupDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	opt := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Optional: true, Description: desc}
	}
	resp.Schema = schema.Schema{
		Description: "The instance the node picks for a service, by the same rules as clients and DNS.",
		Attributes: map[string]schema.Attribute{
			"name":      schema.StringAttribute{Required: true, Description: "Service name."},
			"region":    opt("Preferred region."),
			"key":       opt("Consistent-hashing key: the same key keeps picking the same instance."),
			"tags":      schema.ListAttribute{Optional: true, ElementType: types.StringType, Description: "Required tags."},
			"stale":     opt("include, prefer-live or exclude instances whose owner is not alive."),
			"latency":   opt("nearest or off."),
			"tolerance": opt("With latency=nearest, a Go duration such as 10ms."),
			"address":   schema.StringAttribute{Computed: true, Description: "Address of the selected instance."},
			"instance":  schema.SingleNestedAttribute{Computed: true, Description: "The selected instance.", Attributes: instanceAttributes()},
		},
	}
}

func (d *lookupDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m lookupModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !d.ready(resp) {
		return
	}
	opts := atlas.LookupOptions{Region: m.Region.ValueString(), Key: m.Key.ValueString(), Tags: m.Tags, Stale: m.Stale.ValueString(), Latency: m.Latency.ValueString()}
	if t := m.Tolerance.ValueString(); t != "" {
		tolerance, err := time.ParseDuration(t)
		if err != nil {
			resp.Diagnostics.AddError("Invalid tolerance", err.Error())
			return
		}
		opts.Tolerance = tolerance
	}
	s, err := d.client.LookupWithOptions(ctx, m.Name.ValueString(), opts)
	if err != nil {
		resp.Diagnostics.AddError("Lookup failed", describe(err))
		return
	}
	inst := toInstance(s)
	m.Instance = &inst
	m.Address = types.StringValue(s.Address)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// --- atlas_members ---------------------------------------------------------

type membersDataSource struct{ base }

type memberModel struct {
	ID      types.String `tfsdk:"id"`
	Addr    types.String `tfsdk:"addr"`
	APIAddr types.String `tfsdk:"api_addr"`
	Region  types.String `tfsdk:"region"`
	State   types.String `tfsdk:"state"`
}

type membersModel struct {
	Members []memberModel `tfsdk:"members"`
	Alive   []string      `tfsdk:"alive"`
}

func newMembersDataSource() datasource.DataSource { return &membersDataSource{} }

func (d *membersDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_members"
}

func memberAttributes() map[string]schema.Attribute {
	s := func(d string) schema.StringAttribute { return schema.StringAttribute{Computed: true, Description: d} }
	return map[string]schema.Attribute{
		"id": s("Node ID."), "addr": s("Gossip address."), "api_addr": s("API base URL."),
		"region": s("Region."), "state": s("alive, suspect, dead or left."),
	}
}

func (d *membersDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Cluster members as seen by the node.",
		Attributes: map[string]schema.Attribute{
			"alive":   schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "IDs of alive members, sorted."},
			"members": schema.ListNestedAttribute{Computed: true, Description: "Every member.", NestedObject: schema.NestedAttributeObject{Attributes: memberAttributes()}},
		},
	}
}

func (d *membersDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(resp) {
		return
	}
	members, err := d.client.Members(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Listing members failed", describe(err))
		return
	}
	sort.Slice(members, func(i, j int) bool { return members[i].ID < members[j].ID })
	m := membersModel{Members: []memberModel{}, Alive: []string{}}
	for _, member := range members {
		m.Members = append(m.Members, memberModel{
			ID: types.StringValue(member.ID), Addr: types.StringValue(member.Addr), APIAddr: types.StringValue(member.APIAddr),
			Region: types.StringValue(member.Region), State: types.StringValue(member.State),
		})
		if member.State == "alive" {
			m.Alive = append(m.Alive, member.ID)
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// --- atlas_node ------------------------------------------------------------

type nodeDataSource struct{ base }

type nodeModel struct {
	ID      types.String `tfsdk:"id"`
	Region  types.String `tfsdk:"region"`
	Addr    types.String `tfsdk:"addr"`
	Version types.String `tfsdk:"version"`
	Commit  types.String `tfsdk:"commit"`
}

func newNodeDataSource() datasource.DataSource { return &nodeDataSource{} }

func (d *nodeDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_node"
}

func (d *nodeDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	s := func(d string) schema.StringAttribute { return schema.StringAttribute{Computed: true, Description: d} }
	resp.Schema = schema.Schema{
		Description: "The node the provider is connected to.",
		Attributes:  map[string]schema.Attribute{"id": s("Node ID."), "region": s("Region."), "addr": s("Gossip address."), "version": s("Atlas version."), "commit": s("Build commit.")},
	}
}

func (d *nodeDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(resp) {
		return
	}
	info, err := d.client.Node(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Reading the node failed", describe(err))
		return
	}
	m := nodeModel{
		ID: types.StringValue(info.Node.ID), Region: types.StringValue(info.Node.Region), Addr: types.StringValue(info.Node.Addr),
		Version: types.StringValue(info.Version), Commit: types.StringValue(info.Commit),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
