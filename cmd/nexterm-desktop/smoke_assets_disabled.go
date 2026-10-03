//go:build !smoke

package main

import "net/http"

func desktopSmokeAssetHandler(handler http.Handler) http.Handler { return handler }
