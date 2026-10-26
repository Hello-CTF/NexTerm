//go:build android || ios

package localhelper

// Supported reports whether this build can run a local helper. Mobile apps
// cannot keep a detached process alive, so their local terminals stay regular.
const Supported = false
