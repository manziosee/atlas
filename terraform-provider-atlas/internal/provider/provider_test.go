package provider

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/manziosee/atlas/pkg/atlas"
)

// Acceptance tests run real Terraform against a live node:
//
//	TF_ACC=1 ATLAS_ADDR=http://127.0.0.1:8080 ATLAS_TOKEN=<admin token> go test ./...
var factories = map[string]func() (tfprotov6.ProviderServer, error){
	"atlas": providerserver.NewProtocol6WithError(New("test")()),
}

func preCheck(t *testing.T) {
	t.Helper()
	if os.Getenv("ATLAS_ADDR") == "" || os.Getenv("ATLAS_TOKEN") == "" {
		t.Fatal("acceptance tests need ATLAS_ADDR and an admin ATLAS_TOKEN")
	}
}

func apiClient(t *testing.T) *atlas.Client {
	t.Helper()
	c, err := atlas.NewClient(os.Getenv("ATLAS_ADDR"), os.Getenv("ATLAS_TOKEN"), nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func config(id, version string, draining bool) string {
	return fmt.Sprintf(`
provider "atlas" {}

resource "atlas_service" "web" {
  id           = %[1]q
  name         = "tf-web"
  address      = "127.0.0.1:9"
  version      = %[2]q
  tags         = ["terraform", "web"]
  health_check = "tcp://127.0.0.1:9"
  draining     = %[3]t
}

data "atlas_services" "web" {
  name       = atlas_service.web.name
  tags       = ["terraform"]
  depends_on = [atlas_service.web]
}

data "atlas_service" "web" {
  id = atlas_service.web.id
}

data "atlas_members" "all" {}
data "atlas_node" "this" {}
`, id, version, draining)
}

func TestAccService(t *testing.T) {
	id := fmt.Sprintf("tf-acc-%d", time.Now().UnixNano())
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: factories,
		CheckDestroy: func(*terraform.State) error {
			if _, err := apiClient(t).Get(context.Background(), id); !atlas.IsCode(err, "not_found") {
				return fmt.Errorf("service %s still exists after destroy: %w", id, err)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: config(id, "1.0.0", false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("atlas_service.web", "id", id),
					resource.TestCheckResourceAttr("atlas_service.web", "version", "1.0.0"),
					resource.TestCheckResourceAttr("atlas_service.web", "tags.#", "2"),
					resource.TestCheckResourceAttr("atlas_service.web", "draining", "false"),
					resource.TestCheckResourceAttrSet("atlas_service.web", "node_id"),
					resource.TestCheckResourceAttr("data.atlas_services.web", "instances.#", "1"),
					resource.TestCheckResourceAttr("data.atlas_services.web", "addresses.0", "127.0.0.1:9"),
					resource.TestCheckResourceAttr("data.atlas_service.web", "health_check", "tcp://127.0.0.1:9"),
					resource.TestCheckResourceAttrSet("data.atlas_members.all", "alive.0"),
					resource.TestCheckResourceAttrSet("data.atlas_node.this", "id"),
				),
			},
			{
				// In-place update through PUT: no replacement, new revision.
				Config: config(id, "1.1.0", true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("atlas_service.web", "id", id),
					resource.TestCheckResourceAttr("atlas_service.web", "version", "1.1.0"),
					resource.TestCheckResourceAttr("atlas_service.web", "draining", "true"),
				),
			},
			{
				ResourceName:      "atlas_service.web",
				ImportState:       true,
				ImportStateVerify: true,
				// Read-time values that change on their own.
				ImportStateVerifyIgnore: []string{"health", "revision", "updated_at"},
			},
			{
				// Deleted outside Terraform: the next apply registers it again.
				PreConfig: func() {
					if err := apiClient(t).Delete(context.Background(), id); err != nil {
						t.Fatal(err)
					}
				},
				Config: config(id, "1.1.0", true),
				Check: func(*terraform.State) error {
					_, err := apiClient(t).Get(context.Background(), id)
					return err
				},
			},
		},
	})
}

func TestAccLookup(t *testing.T) {
	id := fmt.Sprintf("tf-acc-lookup-%d", time.Now().UnixNano())
	client := apiClient(t)
	preCheck(t)
	// A healthy TTL instance for the lookup to pick.
	if _, err := client.Register(context.Background(), atlas.RegisterRequest{ID: id, Name: id, Address: "127.0.0.1:10", TTLSeconds: 60, Tags: []string{"primary"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Heartbeat(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Delete(context.Background(), id) })

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "atlas_lookup" "pick" {
  name = %q
  tags = ["primary"]
  key  = "user-42"
}`, id),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.atlas_lookup.pick", "address", "127.0.0.1:10"),
				resource.TestCheckResourceAttr("data.atlas_lookup.pick", "instance.health", "healthy"),
			),
		}},
	})
}

func TestValidateConflictingHealth(t *testing.T) {
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config: `
provider "atlas" { address = "http://127.0.0.1:1" }
resource "atlas_service" "bad" {
  name         = "x"
  address      = "127.0.0.1:9"
  health_check = "tcp://127.0.0.1:9"
  ttl_seconds  = 30
}`,
			PlanOnly:    true,
			ExpectError: regexpMust("Use either health_check or ttl_seconds"),
		}},
	})
}

func regexpMust(s string) *regexp.Regexp { return regexp.MustCompile(regexp.QuoteMeta(s)) }
