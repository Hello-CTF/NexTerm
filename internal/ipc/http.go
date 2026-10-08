package ipc

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const DefaultMaxRPCBytes int64 = 256 << 20

type RPCHandler struct {
	Dispatcher  *Dispatcher
	Environment Environment
	MaxBytes    int64
}

func NewRPCHandler(dispatcher *Dispatcher, environment Environment) *RPCHandler {
	return &RPCHandler{
		Dispatcher:  dispatcher,
		Environment: environment,
		MaxBytes:    DefaultMaxRPCBytes,
	}
}

func (h *RPCHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	maxBytes := h.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxRPCBytes
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	decoder := json.NewDecoder(r.Body)
	var request Request
	if err := decoder.Decode(&request); err != nil {
		_ = json.NewEncoder(w).Encode(Failure(badDecode(err)))
		return
	}
	if err := ensureJSONEnd(decoder); err != nil {
		_ = json.NewEncoder(w).Encode(Failure(badDecode(err)))
		return
	}

	environment := h.Environment
	if clientID := r.Header.Get("X-NexTerm-Client-Id"); clientID != "" {
		environment.ClientID = clientID
	}
	_ = json.NewEncoder(w).Encode(h.Dispatcher.Dispatch(r.Context(), request, environment))
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("request body must contain one JSON value")
		}
		return err
	}
	return nil
}
