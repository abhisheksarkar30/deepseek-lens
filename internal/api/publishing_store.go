package api

import (
	"context"

	"github.com/abhisheksarkar30/deepseek-lens/internal/store"
)

// PublishingStore wraps a *store.Store and satisfies internal/consumer's
// narrow Store interface (InsertRequest, InsertWarnings), publishing a
// Broker event after each successful write.
//
// Deliberate deviation from the bead's literal prose, flagged per its own
// instructions: the bead describes "when the consumer commits a batch it
// publishes {type:"request", id} and {type:"warnings", ...} to an in-process
// broker," which reads as consumer.go doing the publishing. But the bead's
// own Files-to-Touch list does not include internal/consumer/consumer.go,
// and consumer.New's Store parameter is already a narrow interface
// (InsertRequest/InsertWarnings) that anything satisfies structurally — so
// this decorator gets the same externally-observable behavior (an event
// per committed request/warnings write) by wrapping the store consumer.go
// writes through, instead of changing consumer.go itself. serve.go
// constructs one of these and hands it to consumer.New in place of the
// bare *store.Store; the read-only internal/api handlers keep using the
// bare *store.Store directly, so a read path can never trigger a publish.
type PublishingStore struct {
	*store.Store
	broker *Broker
}

// NewPublishingStore returns a PublishingStore wrapping st, publishing to b.
func NewPublishingStore(st *store.Store, b *Broker) *PublishingStore {
	return &PublishingStore{Store: st, broker: b}
}

// InsertRequest inserts r through the wrapped store, then publishes a
// {type:"request", id} event on success.
func (p *PublishingStore) InsertRequest(ctx context.Context, r *store.Request) (int64, error) {
	id, err := p.Store.InsertRequest(ctx, r)
	if err != nil {
		return id, err
	}
	p.broker.Publish(Event{Type: "request", ID: id})
	return id, nil
}

// InsertWarnings inserts warnings for reqID through the wrapped store, then
// publishes a {type:"warnings", id, warnings} event on success. A call with
// no warnings (the common case) is a no-op in the wrapped store and
// publishes nothing.
func (p *PublishingStore) InsertWarnings(ctx context.Context, reqID int64, warnings []store.Warning) error {
	if err := p.Store.InsertWarnings(ctx, reqID, warnings); err != nil {
		return err
	}
	if len(warnings) == 0 {
		return nil
	}
	p.broker.Publish(Event{Type: "warnings", ID: reqID, Warnings: warnings})
	return nil
}
