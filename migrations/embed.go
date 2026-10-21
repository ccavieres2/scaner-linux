// Package migrations embebe las migraciones SQL versionadas.
package migrations

import "embed"

//go:embed *.up.sql
var FS embed.FS
