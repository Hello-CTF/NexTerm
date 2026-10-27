package skills

import (
	"embed"
	"io/fs"
)

//go:embed builtin/*.md
var builtinFS embed.FS

// defaultIndex builds the read-only built-in skills shipped with NexTerm.
// Per-owner overrides, disabling and custom skills layer on top through the
// Manager; the embedded content itself is never modified.
func defaultIndex() (*Index, error) {
	sub, err := fs.Sub(builtinFS, "builtin")
	if err != nil {
		return nil, err
	}
	return BuildIndex(sub)
}
