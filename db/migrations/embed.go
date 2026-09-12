package migrations

import "embed"

// Files contains the immutable SQL migration files shipped with the service.
// The migration runner sorts files by their numeric prefix.
//
//go:embed *.up.sql
var Files embed.FS
