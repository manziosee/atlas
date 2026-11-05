// Command terraform-provider-atlas is the Terraform provider for Atlas
// service discovery.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/manziosee/atlas/terraform-provider-atlas/internal/provider"
)

// version is set by the release build.
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run with support for debuggers like delve")
	flag.Parse()
	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/manziosee/atlas",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
