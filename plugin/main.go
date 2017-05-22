package main

import (
	extras "github.com/spirius/terraform-provider-extras"

	"github.com/hashicorp/terraform/plugin"
)

func main() {
	plugin.Serve(&plugin.ServeOpts{
		ProviderFunc: extras.Provider,
	})
}
