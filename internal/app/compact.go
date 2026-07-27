package app

import (
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
)

// tuiEventDrainMax bounds how many queued events the TUI
// subscription drains in one go. It matches the pubsub broker's
// per-subscriber buffer size: draining more than that in a single
// pass is impossible in practice, and the cap prevents a fast
// producer from starving the forward loop with a never-empty
// channel.
const tuiEventDrainMax = 4096

// drainEvents non-blockingly drains whatever is already queued in ch,
// up to max events, and reports whether the channel was closed
// mid-drain. It never waits: the moment the channel is momentarily
// empty it returns, so a fast producer cannot keep it draining
// indefinitely.
//
// The TUI subscription uses this to grab the whole backlog that piled
// up while Bubble Tea was busy rendering a slow frame, so the backlog
// can be compacted before it is replayed one tea.Msg at a time
// through the (unbuffered, render-per-message) program channel.
func drainEvents(ch <-chan pubsub.Event[tea.Msg], max int) (batch []pubsub.Event[tea.Msg], closed bool) {
	for len(batch) < max {
		select {
		case ev, ok := <-ch:
			if !ok {
				return batch, true
			}
			batch = append(batch, ev)
		default:
			return batch, false
		}
	}
	return batch, false
}

// compactMessageUpdates drops message UpdatedEvents that are
// superseded by a newer update for the same message later in the
// batch. Every message UpdatedEvent payload is a complete snapshot
// of the message state, so when several updates for the same
// message queue up behind a slow consumer, the stale intermediates
// carry no information the newest one does not — replaying them
// only burns Update+render cycles on frames that are already out of
// date.
//
// All other events pass through untouched and in their original
// order: Created/Deleted events (structural, not snapshots),
// UpdatedEvents that are the newest for their message, and every
// non-message event. Terminal message updates are safe to drop when
// a newer update exists because the finish state is part of the
// cumulative snapshot the newer update also carries.
func compactMessageUpdates(batch []pubsub.Event[tea.Msg]) []pubsub.Event[tea.Msg] {
	// Last batch index of an UpdatedEvent per message ID.
	last := make(map[string]int, len(batch))
	messageUpdates := 0
	for i, ev := range batch {
		if e, ok := ev.Payload.(pubsub.Event[message.Message]); ok && e.Type == pubsub.UpdatedEvent {
			last[e.Payload.ID] = i
			messageUpdates++
		}
	}
	if messageUpdates == len(last) {
		// Every update is the newest for its message: nothing to drop.
		return batch
	}
	out := batch[:0]
	for i, ev := range batch {
		if e, ok := ev.Payload.(pubsub.Event[message.Message]); ok &&
			e.Type == pubsub.UpdatedEvent && last[e.Payload.ID] != i {
			continue
		}
		out = append(out, ev)
	}
	return out
}
