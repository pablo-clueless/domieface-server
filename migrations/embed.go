// Package migrations embeds the SQL schema files so a built binary can bring
// its own database up to date with no files to ship alongside it.
package migrations

import "embed"

// FS holds every migration, applied in filename order.
//
//go:embed *.sql
var FS embed.FS
