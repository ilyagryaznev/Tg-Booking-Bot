// Package migrations embeds the SQL schema into the binary,
// so the bot ships as a single file with no external assets.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
