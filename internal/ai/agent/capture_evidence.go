package agent

import (
	"context"
	"errors"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// ErrCaptureUntrusted rejects sealing a capture that did not come from the
// trusted recorder's recording path, such as a decoded or externally
// constructed one.
var ErrCaptureUntrusted = errors.New("capture 不是受信录制路径的产物，不能签发")

// ErrCaptureAlreadySanitized rejects re-sanitizing a sanitized copy:
// sanitized evidence is a terminal review artifact, and a second pass
// would orphan its persisted anchor.
var ErrCaptureAlreadySanitized = errors.New("capture 已是脱敏副本，不能再次脱敏")

// EvidenceStore persists the recorder's key and issued anchors across
// restarts. It is satisfied by *store.Store: the key is created once per
// owner and reloaded afterwards, so anchors issued before a restart still
// verify.
type EvidenceStore interface {
	EvidenceSignerKey(ctx context.Context) ([]byte, error)
	PutEvidenceAnchor(ctx context.Context, captureID, chainHead, envelopeHash, mac string) error
	EvidenceAnchor(ctx context.Context, captureID, chainHead string) (envelopeHash, mac string, err error)
}

// EvidenceRecorder is the recorder-side seal authority. Only it creates
// trusted recordings and seals them; it is held by the recording path and
// never handed to evidence consumers, who receive an EvidenceVerifier
// instead and cannot mint provenance.
type EvidenceRecorder struct {
	store EvidenceStore
}

func NewEvidenceRecorder(store EvidenceStore) *EvidenceRecorder {
	return &EvidenceRecorder{store: store}
}

// NewRecording starts a trusted recording session. Only captures created
// here can be sealed.
func (r *EvidenceRecorder) NewRecording(id, conversationID, jobID string, startedAt int64) *Capture {
	capture := NewCapture(id, conversationID, jobID, startedAt)
	capture.trusted = true
	return capture
}

// Seal seals a trusted capture with the internally held key and persists
// its anchor. Decoded or otherwise untrusted captures are refused:
// re-issuing provenance for them would let a truncated capture mint
// evidence.
func (r *EvidenceRecorder) Seal(ctx context.Context, capture *Capture) (*ProvenanceAnchor, error) {
	if capture == nil || !capture.trusted {
		return nil, ErrCaptureUntrusted
	}
	key, err := r.store.EvidenceSignerKey(ctx)
	if err != nil {
		return nil, err
	}
	anchor, err := capture.seal(key)
	if err != nil {
		return nil, err
	}
	if err := r.store.PutEvidenceAnchor(ctx, anchor.CaptureID, anchor.ChainHead, anchor.EnvelopeHash, anchor.MAC); err != nil {
		return nil, err
	}
	return anchor, nil
}

// EvidenceVerifier is the review-side authority: it resolves trusted
// anchors and replays or sanitizes evidence. It holds no seal capability
// and cannot create trusted recordings, so an evidence caller cannot mint
// provenance.
type EvidenceVerifier struct {
	store EvidenceStore
}

func NewEvidenceVerifier(store EvidenceStore) *EvidenceVerifier {
	return &EvidenceVerifier{store: store}
}

// Replay resolves the persisted anchor internally and replays verified
// evidence. The caller supplies only the capture; the anchor reference is
// its ID plus its current chain head, which a moved capture cannot match.
func (v *EvidenceVerifier) Replay(ctx context.Context, capture *Capture) ([]CaptureEvent, error) {
	if capture == nil {
		return nil, ipc.NewError(ipc.CodeBadParam, "capture 不能为空")
	}
	if !capture.Sealed {
		return nil, ErrCaptureNotSealed
	}
	key, anchor, err := v.resolveAnchor(ctx, capture)
	if err != nil {
		return nil, err
	}
	return capture.replayAnchored(key, anchor)
}

// Sanitize resolves the persisted anchor internally and produces the
// review copy; the sanitized copy's new anchor is issued by the trusted
// recorder path and persisted, never by the caller. Sanitized copies are
// terminal: re-sanitizing one is rejected instead of orphaning its
// anchor.
func (v *EvidenceVerifier) Sanitize(ctx context.Context, capture *Capture) (*Capture, *ProvenanceAnchor, error) {
	if capture == nil {
		return nil, nil, ipc.NewError(ipc.CodeBadParam, "capture 不能为空")
	}
	if !capture.Sealed {
		return nil, nil, ErrCaptureNotSealed
	}
	key, anchor, err := v.resolveAnchor(ctx, capture)
	if err != nil {
		return nil, nil, err
	}
	sanitized, sanitizedAnchor, err := capture.sanitizeAnchored(key, anchor)
	if err != nil {
		return nil, nil, err
	}
	if err := v.store.PutEvidenceAnchor(ctx, sanitizedAnchor.CaptureID, sanitizedAnchor.ChainHead, sanitizedAnchor.EnvelopeHash, sanitizedAnchor.MAC); err != nil {
		return nil, nil, err
	}
	return sanitized, sanitizedAnchor, nil
}

func (v *EvidenceVerifier) resolveAnchor(ctx context.Context, capture *Capture) ([]byte, *ProvenanceAnchor, error) {
	key, err := v.store.EvidenceSignerKey(ctx)
	if err != nil {
		return nil, nil, err
	}
	envelopeHash, mac, err := v.store.EvidenceAnchor(ctx, capture.ID, capture.ChainHead)
	if err != nil {
		if errors.Is(err, store.ErrEvidenceAnchorNotFound) {
			return nil, nil, ipc.WrapError(ipc.CodeNotFound, ErrCaptureAnchorMissing.Error(), ErrCaptureAnchorMissing)
		}
		return nil, nil, err
	}
	anchor := &ProvenanceAnchor{CaptureID: capture.ID, ChainHead: capture.ChainHead, EnvelopeHash: envelopeHash, MAC: mac}
	return key, anchor, nil
}
