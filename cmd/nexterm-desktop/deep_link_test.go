package main

import (
	"reflect"
	"testing"
)

func TestExtractDeepLink(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantArgs []string
		wantLink string
	}{
		{name: "empty", args: nil, wantArgs: nil, wantLink: ""},
		{
			name:     "no link",
			args:     []string{"--auth", "off"},
			wantArgs: []string{"--auth", "off"},
			wantLink: "",
		},
		{
			name:     "link only",
			args:     []string{"nexterm://connect/a-1"},
			wantArgs: []string{},
			wantLink: "nexterm://connect/a-1",
		},
		{
			name:     "link between flags",
			args:     []string{"--auth", "off", "nexterm://connect/a-1", "--listen", "x"},
			wantArgs: []string{"--auth", "off", "--listen", "x"},
			wantLink: "nexterm://connect/a-1",
		},
		{
			name:     "first link wins",
			args:     []string{"nexterm://connect/a-1", "nexterm://connect/a-2"},
			wantArgs: []string{"nexterm://connect/a-2"},
			wantLink: "nexterm://connect/a-1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotArgs, gotLink := extractDeepLink(tc.args)
			if !reflect.DeepEqual(gotArgs, tc.wantArgs) && len(gotArgs)+len(tc.wantArgs) > 0 {
				t.Fatalf("args = %v, want %v", gotArgs, tc.wantArgs)
			}
			if gotLink != tc.wantLink {
				t.Fatalf("link = %q, want %q", gotLink, tc.wantLink)
			}
		})
	}
}

func TestFindDeepLink(t *testing.T) {
	if got := findDeepLink([]string{"/usr/bin/nexterm-desktop", "nexterm://connect/a-1"}); got != "nexterm://connect/a-1" {
		t.Fatalf("got %q", got)
	}
	if got := findDeepLink([]string{"/usr/bin/nexterm-desktop"}); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestDeepLinkRelayBuffersUntilConsumed(t *testing.T) {
	var relay deepLinkRelay
	emitted := 0
	relay.onEmit(func(string) { emitted++ })

	relay.deliver("nexterm://connect/a-1")
	relay.deliver("nexterm://connect/a-2")
	if emitted != 0 {
		t.Fatalf("emitted %d before frontend ready", emitted)
	}
	got := relay.consume()
	if !reflect.DeepEqual(got, []string{"nexterm://connect/a-1", "nexterm://connect/a-2"}) {
		t.Fatalf("consume = %v", got)
	}
	if rest := relay.consume(); len(rest) != 0 {
		t.Fatalf("second consume = %v", rest)
	}

	relay.deliver("nexterm://connect/a-3")
	if emitted != 1 {
		t.Fatalf("emitted %d after ready", emitted)
	}
}
