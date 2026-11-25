// Package migrations embeds the numbered SQL migration files.
package migrations

import "embed"

// FS holds every *.sql file in this directory. Files are applied in
// lexical order, so keep the 0001_, 0002_ prefixes.
//
//go:embed *.sql
var FS embed.FS
