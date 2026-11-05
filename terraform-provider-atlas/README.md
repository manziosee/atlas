# Terraform provider for Atlas

Manage [Atlas](../README.md) service registrations as code, and read the
registry, lookups and cluster membership from Terraform.

| Kind | Name | Use |
|------|------|-----|
| Resource | `atlas_service` | Register an instance (databases, appliances, anything that can't register itself) |
| Data source | `atlas_services` | Every instance matching filters, e.g. to feed load balancers |
| Data source | `atlas_service` | One instance by ID |
| Data source | `atlas_lookup` | The instance Atlas would route to |
| Data source | `atlas_members` | Cluster members |
| Data source | `atlas_node` | The connected node |

See [docs/](docs/) and [examples/main.tf](examples/main.tf).

## Use it before the Registry release

Build it, then point Terraform at the binary:

```bash
cd terraform-provider-atlas
go build -o ~/go/bin/terraform-provider-atlas .
```

```hcl
# ~/.terraformrc (on Windows: %APPDATA%/terraform.rc)
provider_installation {
  dev_overrides {
    "manziosee/atlas" = "/home/you/go/bin"
  }
  direct {}
}
```

## Test

Unit tests run anywhere. Acceptance tests run Terraform against a live node:

```bash
go test ./...
TF_ACC=1 ATLAS_ADDR=http://127.0.0.1:8080 ATLAS_TOKEN=<admin token> go test ./...
```

## Publishing to the Terraform Registry

The Registry only publishes from a public repository named
`terraform-provider-atlas`, with GPG-signed GitHub releases. This folder is
ready to become that repository:

1. Create `manziosee/terraform-provider-atlas` and push this folder's
   contents to it. Replace the `replace` line in `go.mod` with a tagged
   `github.com/manziosee/atlas` version.
2. Create a GPG key. Add the private key and passphrase as the
   `GPG_PRIVATE_KEY` and `PASSPHRASE` repository secrets.
3. Copy `release.yml.example` to `.github/workflows/release.yml` there.
   Then push a tag such as `v0.1.0`; GoReleaser builds and signs the release.
4. Sign in to registry.terraform.io with GitHub, choose **Publish →
   Provider**, and add the public GPG key.
