// Package web embeds the static frontend assets into the compiled binary.
package web

import "embed"

//go:embed static
var Static embed.FS
