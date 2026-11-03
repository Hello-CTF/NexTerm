package production

import "testing"

func TestPreviewModuleIsDesktopOnly(t *testing.T) {
	if _, ok := previewModule(false); ok {
		t.Fatal("preview module must not be provided outside the desktop shell")
	}
	module, ok := previewModule(true)
	if !ok || module.Name != "preview" || module.RegisterCommands == nil {
		t.Fatalf("desktop preview module = %+v, ok=%v", module, ok)
	}
}
