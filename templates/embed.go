// Package templates provides the embedded template filesystem for Petri generators.
// All IaC, GitOps, and application manifest templates are compiled into the binary.
package templates

import "embed"

// FS is the embedded filesystem containing all Petri templates.
// Templates are organised by tool: terraform/, pulumi/, gitops/, apps/.
//
//go:embed terraform pulumi gitops apps
var FS embed.FS
