// Package provider implements the Atlas Terraform provider: services as
// resources, and the cluster, registry and lookups as data sources.
package provider

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/manziosee/atlas/pkg/atlas"
)

var _ provider.Provider = (*atlasProvider)(nil)

type atlasProvider struct{ version string }

// New returns the provider factory Terraform calls.
func New(version string) func() provider.Provider {
	return func() provider.Provider { return &atlasProvider{version: version} }
}

type providerModel struct {
	Address    types.String `tfsdk:"address"`
	Token      types.String `tfsdk:"token"`
	CAFile     types.String `tfsdk:"ca_file"`
	ClientCert types.String `tfsdk:"client_cert"`
	ClientKey  types.String `tfsdk:"client_key"`
	Timeout    types.String `tfsdk:"timeout"`
}

func (p *atlasProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "atlas"
	resp.Version = p.version
}

func (p *atlasProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manage Atlas service-discovery registrations as code, and read cluster state.",
		Attributes: map[string]schema.Attribute{
			"address": schema.StringAttribute{
				Optional:    true,
				Description: "Base URL of any Atlas node's HTTP API, for example https://atlas.example.com:8080. Defaults to ATLAS_ADDR, then http://127.0.0.1:8080.",
			},
			"token": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "API bearer token. Resources need the write scope; data sources need read. Defaults to ATLAS_TOKEN.",
			},
			"ca_file":     schema.StringAttribute{Optional: true, Description: "CA bundle for HTTPS. Defaults to ATLAS_CA_FILE."},
			"client_cert": schema.StringAttribute{Optional: true, Description: "Client certificate for mTLS. Defaults to ATLAS_CLIENT_CERT."},
			"client_key":  schema.StringAttribute{Optional: true, Sensitive: true, Description: "Client key for mTLS. Defaults to ATLAS_CLIENT_KEY."},
			"timeout":     schema.StringAttribute{Optional: true, Description: "Timeout for each API call, as a Go duration. Default 30s."},
		},
	}
}

func pick(value types.String, env, fallback string) string {
	if !value.IsNull() && !value.IsUnknown() && value.ValueString() != "" {
		return value.ValueString()
	}
	if v := os.Getenv(env); v != "" {
		return v
	}
	return fallback
}

func (p *atlasProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if cfg.Address.IsUnknown() || cfg.Token.IsUnknown() {
		// Values from other resources are not known during planning; data
		// sources and resources report it if they are needed.
		return
	}
	timeout, err := time.ParseDuration(pick(cfg.Timeout, "ATLAS_TIMEOUT", "30s"))
	if err != nil || timeout <= 0 {
		resp.Diagnostics.AddAttributeError(path.Root("timeout"), "Invalid timeout", "Use a Go duration such as 30s.")
		return
	}
	tlsCfg, err := clientTLS(pick(cfg.CAFile, "ATLAS_CA_FILE", ""), pick(cfg.ClientCert, "ATLAS_CLIENT_CERT", ""), pick(cfg.ClientKey, "ATLAS_CLIENT_KEY", ""))
	if err != nil {
		resp.Diagnostics.AddError("Invalid TLS settings", err.Error())
		return
	}
	client, err := atlas.NewClient(pick(cfg.Address, "ATLAS_ADDR", "http://127.0.0.1:8080"), pick(cfg.Token, "ATLAS_TOKEN", ""),
		&http.Client{Timeout: timeout, Transport: &http.Transport{TLSClientConfig: tlsCfg}})
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("address"), "Invalid address", err.Error())
		return
	}
	resp.ResourceData = client
	resp.DataSourceData = client
}

func (p *atlasProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{newServiceResource}
}

func (p *atlasProvider) DataSources(context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		newServiceDataSource, newServicesDataSource, newLookupDataSource, newMembersDataSource, newNodeDataSource,
	}
}
