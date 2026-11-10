//go:build !unix

package store

func matchOwner(string, string) error { return nil }
