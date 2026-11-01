package update

import (
	"fmt"
	"testing"
)

func release(tag string, prerelease, draft bool) Release {
	return Release{TagName: tag, Prerelease: prerelease, Draft: draft}
}

func TestSelectReleaseTable(t *testing.T) {
	cases := []struct {
		name     string
		current  string
		channel  ReleaseChannel
		releases []Release
		want     string
	}{
		{"stable picks newest stable", "1.3.6", ChannelStable, []Release{release("v1.3.7", false, false), release("v1.4.0", false, false), release("v1.3.9", false, false)}, "v1.4.0"},
		{"stable ignores rc", "1.3.6", ChannelStable, []Release{release("v1.4.0-rc.1", true, false), release("v1.3.7", false, false)}, "v1.3.7"},
		{"stable with only rc newer", "1.3.6", ChannelStable, []Release{release("v1.4.0-rc.1", true, false)}, ""},
		{"rc picks rc newer than stable", "1.3.6", ChannelRC, []Release{release("v1.3.7", false, false), release("v1.4.0-rc.2", true, false), release("v1.4.0-rc.1", true, false)}, "v1.4.0-rc.2"},
		{"rc orders rc.10 above rc.2", "1.3.6", ChannelRC, []Release{release("v1.4.0-rc.2", true, false), release("v1.4.0-rc.10", true, false), release("v1.4.0-rc.9", true, false)}, "v1.4.0-rc.10"},
		{"rc prefers final over its rc", "1.3.6", ChannelRC, []Release{release("v1.4.0-rc.3", true, false), release("v1.4.0", false, false)}, "v1.4.0"},
		{"rc channel takes newer stable over older rc", "1.3.6", ChannelRC, []Release{release("v1.4.0-rc.3", true, false), release("v1.5.0", false, false)}, "v1.5.0"},
		{"running rc on rc moves to next rc", "1.4.0-rc.1", ChannelRC, []Release{release("v1.4.0-rc.2", true, false)}, "v1.4.0-rc.2"},
		{"running rc on rc skips older rc", "1.4.0-rc.5", ChannelRC, []Release{release("v1.4.0-rc.2", true, false)}, ""},
		{"running rc on stable ignores rc", "1.4.0-rc.1", ChannelStable, []Release{release("v1.4.0-rc.2", true, false)}, ""},
		{"running rc on stable never downgrades", "1.4.0-rc.1", ChannelStable, []Release{release("v1.3.6", false, false)}, ""},
		{"running rc on stable takes newer stable", "1.4.0-rc.1", ChannelStable, []Release{release("v1.3.6", false, false), release("v1.4.0", false, false)}, "v1.4.0"},
		{"running rc on stable skips same-version final only when newer", "1.4.0-rc.2", ChannelStable, []Release{release("v1.4.0", false, false)}, "v1.4.0"},
		{"running stable on rc never downgrades", "1.4.0", ChannelRC, []Release{release("v1.4.0-rc.9", true, false)}, ""},
		{"non-rc prerelease ignored on rc", "1.3.6", ChannelRC, []Release{release("v1.4.0-beta.1", true, false), release("v1.4.0-alpha", true, false), release("v1.4.0-rc", true, false), release("v1.4.0-rc.1.1", true, false)}, ""},
		{"non-rc prerelease ignored on stable", "1.3.6", ChannelStable, []Release{release("v1.4.0-beta.1", true, false)}, ""},
		{"drafts excluded", "1.3.6", ChannelRC, []Release{release("v1.4.0-rc.1", true, true), release("v1.4.0", false, true)}, ""},
		{"prerelease flag without rc tag is not stable", "1.3.6", ChannelRC, []Release{release("v1.4.0", true, false)}, ""},
		{"rc tag not flagged prerelease is ignored", "1.3.6", ChannelRC, []Release{release("v1.4.0-rc.1", false, false)}, ""},
		{"unparseable tag ignored", "1.3.6", ChannelRC, []Release{release("nightly", false, false)}, ""},
		{"dev build offered newest stable", "dev", ChannelStable, []Release{release("v1.4.0-rc.1", true, false), release("v1.3.6", false, false)}, "v1.3.6"},
		{"dev build on rc offered newest", "dev", ChannelRC, []Release{release("v1.4.0-rc.1", true, false), release("v1.3.6", false, false)}, "v1.4.0-rc.1"},
		{"many rc do not hide stable", "1.3.5", ChannelStable, append(manyRC(120), release("v1.3.6", false, false)), "v1.3.6"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			best, ok := selectRelease(test.releases, test.current, test.channel)
			if test.want == "" {
				if ok {
					t.Fatalf("selected %s, want none", best.TagName)
				}
				return
			}
			if !ok || best.TagName != test.want {
				t.Fatalf("selected %q (%v), want %q", best.TagName, ok, test.want)
			}
		})
	}
}

func manyRC(count int) []Release {
	releases := make([]Release, 0, count)
	for index := 1; index <= count; index++ {
		releases = append(releases, release(fmt.Sprintf("v1.2.0-rc.%d", index), true, false))
	}
	return releases
}
