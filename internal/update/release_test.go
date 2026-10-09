package update

import "testing"

func release(tag string, prerelease, draft bool) Release {
	return Release{TagName: tag, Prerelease: prerelease, Draft: draft}
}

func TestSelectReleasePicksNewestAcceptable(t *testing.T) {
	releases := []Release{
		release("v0.2.3", false, false),
		release("v0.3.0", false, false),
		release("v0.2.9", false, false),
	}
	best, ok := selectRelease(releases, "0.2.2")
	if !ok || best.TagName != "v0.3.0" {
		t.Fatalf("selectRelease = %+v, %v", best, ok)
	}
}

func TestSelectReleaseSkipsDraftsAndOlder(t *testing.T) {
	releases := []Release{
		release("v0.3.0", false, true),
		release("v0.2.1", false, false),
		release("v0.2.2", false, false),
	}
	if _, ok := selectRelease(releases, "0.2.2"); ok {
		t.Fatal("draft or non-newer releases must not be selected")
	}
}

func TestSelectReleasePrereleaseFollowsCurrentVersion(t *testing.T) {
	releases := []Release{
		release("v0.3.0-rc.1", true, false),
		release("v0.2.9", false, false),
	}
	best, ok := selectRelease(releases, "0.2.2")
	if !ok || best.TagName != "v0.2.9" {
		t.Fatalf("stable current must skip prerelease, got %+v", best)
	}
	best, ok = selectRelease(releases, "0.2.2-rc.7")
	if !ok || best.TagName != "v0.3.0-rc.1" {
		t.Fatalf("rc current must accept prerelease, got %+v", best)
	}
}

func TestSelectReleaseSkipsPrereleaseOlderThanCurrent(t *testing.T) {
	releases := []Release{release("v0.2.2-rc.1", true, false)}
	if _, ok := selectRelease(releases, "0.2.2"); ok {
		t.Fatal("prerelease must be rejected for stable current")
	}
	if _, ok := selectRelease(releases, "0.2.2-rc.7"); ok {
		t.Fatal("older prerelease must not be selected")
	}
}

func TestSelectReleaseDevAcceptsAnyRelease(t *testing.T) {
	releases := []Release{release("v0.3.0-rc.1", true, false)}
	best, ok := selectRelease(releases, "dev")
	if !ok || best.TagName != "v0.3.0-rc.1" {
		t.Fatalf("dev current must accept prerelease, got %+v", best)
	}
}
