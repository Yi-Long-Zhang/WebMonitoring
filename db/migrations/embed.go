package migrations

import "embed"

// Files contains the immutable SQL migration files shipped with the service.
// The migration runner sorts files by their numeric prefix. The destructive
// sidecar markers (*.destructive) ride along so the runner can see them; they
// are never executed.
//
//go:embed *.up.sql *.destructive
var Files embed.FS
