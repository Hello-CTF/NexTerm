package watches

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Sink receives watchEvent envelopes as untrusted tool data. Implementations
// must dedupe by Data.Event.DeliveryID: delivery is at-least-once, and a
// crash between DeliverWatchToolData and the acknowledgement produces a
// redelivery with the same DeliveryID and RunID.
type Sink interface {
	DeliverWatchToolData(ctx context.Context, data ToolData) error
}

// Dispatcher moves pending outbox rows to the sink. Rows are only read while
// pending; cancel settles them atomically with the watch so cancelled events
// stop being delivered.
type Dispatcher struct {
	store Store
	sink  Sink
	now   func() time.Time
}

func NewDispatcher(store Store, sink Sink, options Options) (*Dispatcher, error) {
	if store == nil {
		return nil, errors.New("watches: dispatcher store is nil")
	}
	if sink == nil {
		return nil, errors.New("watches: dispatcher sink is nil")
	}
	options.withDefaults()
	return &Dispatcher{store: store, sink: sink, now: options.Now}, nil
}

// DispatchOnce delivers up to limit pending events and acknowledges each one.
// A sink failure leaves the row pending for a later retry; an acknowledgement
// race means another dispatcher already settled the row, which is exactly the
// duplicate-delivery window receivers dedupe away.
func (d *Dispatcher) DispatchOnce(ctx context.Context, limit int) (int, error) {
	rows, err := d.store.AgentWatchEventPendingList(ctx, limit)
	if err != nil {
		return 0, err
	}
	delivered := 0
	for _, row := range rows {
		var event Event
		if err := json.Unmarshal([]byte(row.EventJSON), &event); err != nil {
			return delivered, fmt.Errorf("%w: %v", ErrEventCorrupt, err)
		}
		if event.Kind != EventKind || event.DeliveryID != row.DeliveryID || event.WatchID != row.WatchID {
			return delivered, fmt.Errorf("%w: delivery %s envelope mismatch", ErrEventCorrupt, row.DeliveryID)
		}
		if err := d.sink.DeliverWatchToolData(ctx, NewToolData(event)); err != nil {
			return delivered, err
		}
		if _, err := d.store.AgentWatchEventAck(ctx, row.DeliveryID, row.Revision, d.now().UnixMilli()); err != nil {
			if isBenignWatchRace(err) {
				delivered++
				continue
			}
			return delivered, err
		}
		delivered++
	}
	return delivered, nil
}
