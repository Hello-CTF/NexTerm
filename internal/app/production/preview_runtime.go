package production

import "github.com/Hello-CTF/NexTerm/internal/preview"

// previewModule exposes the in-app browser commands; only the desktop shell hosts that browser.
func previewModule(desktop bool) (Module, bool) {
	if !desktop {
		return Module{}, false
	}
	return Module{Name: "preview", RegisterCommands: preview.NewService(true).RegisterCommands}, true
}
