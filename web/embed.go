// Package web embeds the CPAModelDock management UI assets.
package web

import "embed"

// Assets holds the static UI files served as plugin resources.
//
//go:embed assets
var Assets embed.FS
