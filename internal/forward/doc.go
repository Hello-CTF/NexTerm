// Package forward owns SSH static listeners and CONNECT-only SOCKS5 forwarding.
// Listeners survive transport reconnects by resolving the current dialer for every
// new connection. Removing a listener also cancels its established connections.
//
// Set NEXTERM_FORWARD_INTEROP=1 with the M12 SSH test variables and
// NEXTERM_FORWARD_TEST_TARGET=host:port to run the opt-in real SSH forwarding test.
package forward
