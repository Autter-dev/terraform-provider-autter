//go:build tools

// Package tools pins the documentation generator, tfplugindocs. It has its own
// module so its dependencies stay out of the provider's go.mod.
package tools

import (
	_ "github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs"
)
