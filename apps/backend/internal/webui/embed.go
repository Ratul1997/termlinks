package webui

import "embed"

// Files contains the production web client built by apps/web.
//
//go:embed all:dist
var Files embed.FS
