package provider

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/manziosee/atlas/pkg/atlas"
)

var (
	_ resource.Resource                   = (*serviceResource)(nil)
	_ resource.ResourceWithConfigure      = (*serviceResource)(nil)
	_ resource.ResourceWithImportState    = (*serviceResource)(nil)
	_ resource.ResourceWithValidateConfig = (*serviceResource)(nil)
)

type serviceResource struct{ client *atlas.Client }

func newServiceResource() resource.Resource { return &serviceResource{} }

// serviceModel is an atlas_service. Health-check tuning fields are zero
// when unset, matching the API.
type serviceModel struct {
	ID                   types.String `tfsdk:"id"`
	Name                 types.String `tfsdk:"name"`
	Address              types.String `tfsdk:"address"`
	Version              types.String `tfsdk:"version"`
	Tags                 types.Set    `tfsdk:"tags"`
	HealthCheck          types.String `tfsdk:"health_check"`
	TTLSeconds           types.Int64  `tfsdk:"ttl_seconds"`
	CheckIntervalSeconds types.Int64  `tfsdk:"check_interval_seconds"`
	UnhealthyThreshold   types.Int64  `tfsdk:"unhealthy_threshold"`
	HealthyThreshold     types.Int64  `tfsdk:"healthy_threshold"`
	Draining             types.Bool   `tfsdk:"draining"`
	NodeID               types.String `tfsdk:"node_id"`
	Region               types.String `tfsdk:"region"`
	Health               types.String `tfsdk:"health"`
	Revision             types.Int64  `tfsdk:"revision"`
	UpdatedAt            types.String `tfsdk:"updated_at"`
}

func (r *serviceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service"
}

func (r *serviceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	computed := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, Description: desc}
	}
	resp.Schema = schema.Schema{
		Description: "A service instance registered in Atlas. The node the provider talks to owns it and runs its health check. " +
			"Prefer health_check for instances Terraform manages: a ttl_seconds instance expires unless something sends heartbeats.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Optional: true, Computed: true,
				Description:   "Stable instance ID. Generated when omitted. Changing it replaces the instance.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()},
			},
			"name":    schema.StringAttribute{Required: true, Description: "Service name, for example payments."},
			"address": schema.StringAttribute{Required: true, Description: "host:port the instance listens on."},
			"version": schema.StringAttribute{Optional: true, Description: "Version label."},
			"tags":    schema.SetAttribute{Optional: true, ElementType: types.StringType, Description: "Up to 16 tags."},
			"health_check": schema.StringAttribute{
				Optional:    true,
				Description: "http(s)://, tcp:// or grpc(s):// URL on the address, checked by the owning node. Not with ttl_seconds.",
			},
			"ttl_seconds":            schema.Int64Attribute{Optional: true, Description: "Heartbeat TTL. Something else must send heartbeats."},
			"check_interval_seconds": schema.Int64Attribute{Optional: true, Description: "Check interval; the node default when unset."},
			"unhealthy_threshold":    schema.Int64Attribute{Optional: true, Description: "Failed checks before unhealthy (default 3)."},
			"healthy_threshold":      schema.Int64Attribute{Optional: true, Description: "Passing checks before recovery (default 2)."},
			"draining": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Keep the record but send it no traffic.",
			},
			"node_id":    computed("Owning node."),
			"region":     computed("Region of the owning node."),
			"health":     computed("unknown, healthy or unhealthy, as last read. Not tracked as drift."),
			"revision":   schema.Int64Attribute{Computed: true, Description: "Revision as last read."},
			"updated_at": computed("Last change, RFC 3339."),
		},
	}
}

func (r *serviceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*atlas.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("got %T", req.ProviderData))
		return
	}
	r.client = client
}

func (r *serviceResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m serviceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	hasCheck := !m.HealthCheck.IsNull() && m.HealthCheck.ValueString() != ""
	if hasCheck && !m.TTLSeconds.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("ttl_seconds"), "Conflicting health settings", "Use either health_check or ttl_seconds, not both.")
	}
	if !hasCheck && (!m.CheckIntervalSeconds.IsNull() || !m.UnhealthyThreshold.IsNull() || !m.HealthyThreshold.IsNull()) {
		resp.Diagnostics.AddAttributeError(path.Root("health_check"), "Missing health_check", "check_interval_seconds and the thresholds need a health_check.")
	}
	if !m.TTLSeconds.IsNull() {
		resp.Diagnostics.AddAttributeWarning(path.Root("ttl_seconds"), "TTL instance",
			"Atlas marks a TTL instance unhealthy after one missed TTL and removes it after three, unless something sends heartbeats. Terraform does not.")
	}
}

func (r *serviceResource) spec(ctx context.Context, m serviceModel) (atlas.RegisterRequest, diag.Diagnostics) {
	var diags diag.Diagnostics
	spec := atlas.RegisterRequest{
		ID:                   m.ID.ValueString(),
		Name:                 m.Name.ValueString(),
		Address:              m.Address.ValueString(),
		Version:              m.Version.ValueString(),
		HealthCheck:          m.HealthCheck.ValueString(),
		TTLSeconds:           m.TTLSeconds.ValueInt64(),
		CheckIntervalSeconds: m.CheckIntervalSeconds.ValueInt64(),
		UnhealthyThreshold:   int(m.UnhealthyThreshold.ValueInt64()),
		HealthyThreshold:     int(m.HealthyThreshold.ValueInt64()),
		Draining:             m.Draining.ValueBool(),
	}
	if m.ID.IsUnknown() {
		spec.ID = ""
	}
	if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
		diags.Append(m.Tags.ElementsAs(ctx, &spec.Tags, false)...)
		sort.Strings(spec.Tags)
	}
	return spec, diags
}

// fill copies the server's view into the model, keeping the user's choice
// of null versus empty for optional fields.
func fill(m *serviceModel, s atlas.Service) {
	m.ID = types.StringValue(s.ID)
	m.Name = types.StringValue(s.Name)
	m.Address = types.StringValue(s.Address)
	optString := func(cur types.String, v string) types.String {
		if v == "" && cur.IsNull() {
			return types.StringNull()
		}
		return types.StringValue(v)
	}
	optInt := func(cur types.Int64, v int64) types.Int64 {
		if v == 0 && cur.IsNull() {
			return types.Int64Null()
		}
		return types.Int64Value(v)
	}
	m.Version = optString(m.Version, s.Version)
	m.HealthCheck = optString(m.HealthCheck, s.HealthCheck)
	m.TTLSeconds = optInt(m.TTLSeconds, s.TTLSeconds)
	m.CheckIntervalSeconds = optInt(m.CheckIntervalSeconds, s.CheckIntervalSeconds)
	m.UnhealthyThreshold = optInt(m.UnhealthyThreshold, int64(s.UnhealthyThreshold))
	m.HealthyThreshold = optInt(m.HealthyThreshold, int64(s.HealthyThreshold))
	if len(s.Tags) == 0 && m.Tags.IsNull() {
		m.Tags = types.SetNull(types.StringType)
	} else {
		values := make([]attr.Value, 0, len(s.Tags))
		for _, t := range s.Tags {
			values = append(values, types.StringValue(t))
		}
		m.Tags = types.SetValueMust(types.StringType, values)
	}
	m.Draining = types.BoolValue(s.Draining)
	m.NodeID = types.StringValue(s.NodeID)
	m.Region = types.StringValue(s.Region)
	m.Health = types.StringValue(string(s.Health))
	m.Revision = types.Int64Value(int64(s.Revision)) // #nosec G115 -- revisions stay far below 2^63.
	m.UpdatedAt = types.StringValue(s.UpdatedAt.UTC().Format(time.RFC3339))
}

func (r *serviceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan serviceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	spec, diags := r.spec(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() || !r.ready(&resp.Diagnostics) {
		return
	}
	created, err := r.client.Register(ctx, spec)
	if err != nil {
		resp.Diagnostics.AddError("Registering the service failed", describe(err))
		return
	}
	fill(&plan, created)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *serviceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state serviceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || !r.ready(&resp.Diagnostics) {
		return
	}
	current, err := r.client.Get(ctx, state.ID.ValueString())
	if atlas.IsCode(err, "not_found") {
		// Expired or deleted outside Terraform: plan to create it again.
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Reading the service failed", describe(err))
		return
	}
	fill(&state, current)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *serviceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state serviceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	plan.ID = state.ID
	spec, diags := r.spec(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() || !r.ready(&resp.Diagnostics) {
		return
	}
	// PUT replaces every field, so settings removed from the configuration
	// are cleared on the server too.
	updated, err := r.client.Replace(ctx, spec.ID, spec, 0)
	if err != nil {
		resp.Diagnostics.AddError("Updating the service failed", describe(err))
		return
	}
	fill(&plan, updated)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *serviceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state serviceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || !r.ready(&resp.Diagnostics) {
		return
	}
	if err := r.client.Delete(ctx, state.ID.ValueString()); err != nil && !atlas.IsCode(err, "not_found") {
		resp.Diagnostics.AddError("Deregistering the service failed", describe(err))
	}
}

func (r *serviceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func (r *serviceResource) ready(diags *diag.Diagnostics) bool {
	if r.client == nil {
		diags.AddError("Provider not configured", "The Atlas provider's address or token was not known; set them directly or with ATLAS_ADDR and ATLAS_TOKEN.")
		return false
	}
	return true
}

// describe turns an API error into advice.
func describe(err error) string {
	switch {
	case atlas.IsCode(err, "unauthorized"):
		return "The node rejected the token. Check the provider's token (or ATLAS_TOKEN). " + err.Error()
	case atlas.IsCode(err, "insufficient_scope"):
		return "The token lacks the scope this needs: services need write, data sources read. " + err.Error()
	case atlas.IsCode(err, "owner_alive"), atlas.IsCode(err, "not_owner"):
		return "Another live node owns this service. Point the provider at that node, or use an admin token to delete it. " + err.Error()
	}
	return err.Error()
}
