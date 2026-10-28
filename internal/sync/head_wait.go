package sync

import (
	"net/http"
	gosync "sync"
	"time"
)

const (
	// maxHeadWait stays under the server's one-minute write idle timeout and
	// the 60-second default of common reverse proxies.
	maxHeadWait     = 25 * time.Second
	minHeadWait     = time.Second
	headWaitRecheck = 2 * time.Second
)

type WaitRequest struct {
	Protocol  int    `json:"protocol"`
	KnownHead string `json:"known_head"`
	TimeoutMS int64  `json:"timeout_ms,omitempty"`
}

type WaitResponse struct {
	Protocol int    `json:"protocol"`
	Head     string `json:"head"`
	Changed  bool   `json:"changed"`
}

// headWatchers wakes long polls when a push or delete moves a user's head in
// this process. Waiters also re-read the head periodically, so a change made
// through another handler or process is still seen.
var headWatchers = struct {
	gosync.Mutex
	byUser map[string]map[chan struct{}]struct{}
}{byUser: map[string]map[chan struct{}]struct{}{}}

func watchHead(userID string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	headWatchers.Lock()
	if headWatchers.byUser[userID] == nil {
		headWatchers.byUser[userID] = map[chan struct{}]struct{}{}
	}
	headWatchers.byUser[userID][ch] = struct{}{}
	headWatchers.Unlock()
	return ch, func() {
		headWatchers.Lock()
		delete(headWatchers.byUser[userID], ch)
		if len(headWatchers.byUser[userID]) == 0 {
			delete(headWatchers.byUser, userID)
		}
		headWatchers.Unlock()
	}
}

func notifyHeadChanged(userID string) {
	headWatchers.Lock()
	defer headWatchers.Unlock()
	for ch := range headWatchers.byUser[userID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// serveWait answers as soon as the head differs from known_head, or reports
// no change when the timeout passes, so clients learn about other devices'
// pushes within moments instead of on their next polling round.
func (h *ObjectHandler) serveWait(w http.ResponseWriter, r *http.Request, userID string) {
	var request WaitRequest
	if !decodeSyncRequest(w, r, &request) {
		return
	}
	if request.Protocol != ProtocolVersion {
		writeSyncError(w, http.StatusBadRequest, protocolMismatchError(request.Protocol))
		return
	}
	timeout := time.Duration(request.TimeoutMS) * time.Millisecond
	if timeout <= 0 || timeout > maxHeadWait {
		timeout = maxHeadWait
	}
	if timeout < minHeadWait {
		timeout = minHeadWait
	}
	// Register before the first read so a push between the read and the wait is not missed.
	changed, stop := watchHead(userID)
	defer stop()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	recheck := time.NewTicker(headWaitRecheck)
	defer recheck.Stop()
	for {
		head, err := h.store.currentHead(r.Context(), userID)
		if err != nil {
			writeSyncError(w, syncErrorStatus(err), err)
			return
		}
		if head != request.KnownHead {
			writeSyncJSON(w, http.StatusOK, WaitResponse{Protocol: ProtocolVersion, Head: head, Changed: true})
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			writeSyncJSON(w, http.StatusOK, WaitResponse{Protocol: ProtocolVersion, Head: head})
			return
		case <-changed:
		case <-recheck.C:
		}
	}
}
