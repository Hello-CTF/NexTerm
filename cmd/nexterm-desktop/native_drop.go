package main

import (
	"os"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const filesDroppedEvent = "files://dropped"

type nativeDroppedPath struct {
	Path string `json:"path"`
	Dir  bool   `json:"dir"`
}

type nativeDropPayload struct {
	Paths []nativeDroppedPath `json:"paths"`
	X     int                 `json:"x"`
	Y     int                 `json:"y"`
}

// Unstatable paths stay files so the upload pipeline surfaces the missing file
// with a real error instead of the drop vanishing silently.
func classifyDroppedPaths(filenames []string) []nativeDroppedPath {
	paths := make([]nativeDroppedPath, 0, len(filenames))
	for _, name := range filenames {
		dir := false
		if info, err := os.Stat(name); err == nil {
			dir = info.IsDir()
		}
		paths = append(paths, nativeDroppedPath{Path: name, Dir: dir})
	}
	return paths
}

// wails emits Common.WindowFilesDropped to Go listeners only; the frontend gets
// the drop through this re-emit with per-path file/dir classification.
func emitFilesDropped(app *application.App, event *application.WindowEvent) {
	ctx := event.Context()
	filenames := ctx.DroppedFiles()
	if len(filenames) == 0 {
		return
	}
	payload := nativeDropPayload{Paths: classifyDroppedPaths(filenames)}
	if target := ctx.DropTargetDetails(); target != nil {
		payload.X = target.X
		payload.Y = target.Y
	}
	app.Event.Emit(filesDroppedEvent, payload)
}
