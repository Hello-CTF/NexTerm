package skills

import "embed"

// embeddedFS ships the built-in skill documents. Built-in content must stay
// free of concrete CLI claims until real captures exist; it currently only
// documents the skill format itself.
//
//go:embed embedded
var embeddedFS embed.FS

func defaultIndex() (*Index, error) {
	return BuildIndex(embeddedFS)
}
