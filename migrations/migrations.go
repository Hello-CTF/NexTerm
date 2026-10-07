package migrations

import "embed"

//go:embed *.sql
var Files embed.FS

//go:embed postgres/*.sql
var PostgresFiles embed.FS
