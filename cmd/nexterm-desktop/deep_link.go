package main

import (
	"strings"
	"sync"
)

const deepLinkScheme = "nexterm://"

const deepLinkEvent = "app://deep-link"

func extractDeepLink(args []string) ([]string, string) {
	for i, arg := range args {
		if strings.HasPrefix(arg, deepLinkScheme) {
			cleaned := make([]string, 0, len(args)-1)
			cleaned = append(cleaned, args[:i]...)
			cleaned = append(cleaned, args[i+1:]...)
			return cleaned, arg
		}
	}
	return args, ""
}

func findDeepLink(args []string) string {
	for _, arg := range args {
		if strings.HasPrefix(arg, deepLinkScheme) {
			return arg
		}
	}
	return ""
}

type deepLinkRelay struct {
	mu      sync.Mutex
	ready   bool
	pending []string
	emit    func(url string)
}

func (r *deepLinkRelay) onEmit(emit func(url string)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.emit = emit
}

func (r *deepLinkRelay) deliver(url string) {
	r.mu.Lock()
	if !r.ready {
		r.pending = append(r.pending, url)
		r.mu.Unlock()
		return
	}
	emit := r.emit
	r.mu.Unlock()
	if emit != nil {
		emit(url)
	}
}

func (r *deepLinkRelay) consume() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ready = true
	links := r.pending
	r.pending = nil
	return links
}
