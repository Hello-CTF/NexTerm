package main

import (
	"testing"

	production "github.com/Hello-CTF/NexTerm/internal/app/production"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func TestScreenInsets(t *testing.T) {
	tests := []struct {
		name   string
		screen *application.Screen
		want   production.ScreenInsets
	}{
		{
			name:   "nil screen yields zero insets",
			screen: nil,
			want:   production.ScreenInsets{},
		},
		{
			name: "portrait status and nav bars",
			screen: &application.Screen{
				Bounds:   application.Rect{X: 0, Y: 0, Width: 393, Height: 873},
				WorkArea: application.Rect{X: 0, Y: 24, Width: 393, Height: 825},
			},
			want: production.ScreenInsets{Top: 24, Bottom: 24},
		},
		{
			name: "landscape notch moves insets to the sides",
			screen: &application.Screen{
				Bounds:   application.Rect{X: 0, Y: 0, Width: 873, Height: 393},
				WorkArea: application.Rect{X: 48, Y: 0, Width: 777, Height: 393},
			},
			want: production.ScreenInsets{Left: 48, Right: 48},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := screenInsets(tt.screen); got != tt.want {
				t.Fatalf("screenInsets() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
