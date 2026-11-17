// Package migrations embeds SQL migrations applied by goose at startup.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
