// Command terraform-provider-autter is the Autter Terraform provider.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/Autter-dev/terraform-provider-autter/internal/provider"
)

// Generate the documentation in docs/ (tfplugindocs, pinned in tools/go.mod).
//go:generate go run -modfile=tools/go.mod github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs generate --provider-name autter

// version is set by GoReleaser at build time.
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider with support for debuggers like delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/autter-dev/autter",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err.Error())
	}
}
