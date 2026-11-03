// Command terraform-provider-matterbridge is an OpenTofu provider. It manages Matterbridge through the WebSocket API of its frontend.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/jellyfrog/terraform-provider-matterbridge/internal/provider"
)

//go:generate sh tools/docs.sh

var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run with support for debuggers, for example delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.opentofu.org/jellyfrog/matterbridge",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
