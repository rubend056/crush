package app

import (
	"context"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

// msgUpdate wraps a message snapshot in the double Event envelope the
// TUI subscription channel carries: a message event as the Payload of
// an app-level tea.Msg event.
func msgUpdate(t pubsub.EventType, id, text string) pubsub.Event[tea.Msg] {
	return pubsub.Event[tea.Msg]{
		Type: pubsub.UpdatedEvent,
		Payload: pubsub.Event[message.Message]{
			Type: t,
			Payload: message.Message{
				ID:    id,
				Role:  message.Assistant,
				Parts: []message.ContentPart{message.TextContent{Text: text}},
			},
		},
	}
}

func msgPayload(t *testing.T, ev pubsub.Event[tea.Msg]) *pubsub.Event[message.Message] {
	t.Helper()
	e, ok := ev.Payload.(pubsub.Event[message.Message])
	require.True(t, ok, "expected a message event, got %T", ev.Payload)
	return &e
}

func TestCompactMessageUpdates_DropsSupersededUpdates(t *testing.T) {
	t.Parallel()

	batch := []pubsub.Event[tea.Msg]{
		msgUpdate(pubsub.UpdatedEvent, "m1", "draft 1"),
		msgUpdate(pubsub.UpdatedEvent, "m1", "draft 2"),
		msgUpdate(pubsub.UpdatedEvent, "m1", "draft 3"),
	}

	out := compactMessageUpdates(batch)
	require.Len(t, out, 1, "all but the newest update for m1 must be dropped")
	require.Equal(t, "draft 3", msgPayload(t, out[0]).Payload.Content().Text,
		"the newest snapshot must survive")
}

func TestCompactMessageUpdates_KeepsNewestPerMessage(t *testing.T) {
	t.Parallel()

	batch := []pubsub.Event[tea.Msg]{
		msgUpdate(pubsub.UpdatedEvent, "m1", "m1 draft 1"),
		msgUpdate(pubsub.UpdatedEvent, "m2", "m2 draft 1"),
		msgUpdate(pubsub.UpdatedEvent, "m1", "m1 draft 2"),
		msgUpdate(pubsub.UpdatedEvent, "m2", "m2 draft 2"),
	}

	out := compactMessageUpdates(batch)
	require.Len(t, out, 2)
	require.Equal(t, "m1 draft 2", msgPayload(t, out[0]).Payload.Content().Text)
	require.Equal(t, "m2 draft 2", msgPayload(t, out[1]).Payload.Content().Text)
	require.Equal(t, "m1", msgPayload(t, out[0]).Payload.ID)
	require.Equal(t, "m2", msgPayload(t, out[1]).Payload.ID,
		"survivors must keep their relative order")
}

func TestCompactMessageUpdates_NeverDropsCreatedOrDeleted(t *testing.T) {
	t.Parallel()

	batch := []pubsub.Event[tea.Msg]{
		msgUpdate(pubsub.CreatedEvent, "m1", "created"),
		msgUpdate(pubsub.UpdatedEvent, "m1", "update 1"),
		msgUpdate(pubsub.UpdatedEvent, "m1", "update 2"),
		msgUpdate(pubsub.DeletedEvent, "m2", "deleted"),
	}

	out := compactMessageUpdates(batch)
	require.Len(t, out, 3, "only the superseded update may be dropped")
	require.Equal(t, pubsub.CreatedEvent, msgPayload(t, out[0]).Type)
	require.Equal(t, "update 2", msgPayload(t, out[1]).Payload.Content().Text)
	require.Equal(t, pubsub.DeletedEvent, msgPayload(t, out[2]).Type)
}

func TestCompactMessageUpdates_PreservesNonMessageEventsInOrder(t *testing.T) {
	t.Parallel()

	sessEvent := pubsub.Event[tea.Msg]{
		Type:    pubsub.UpdatedEvent,
		Payload: pubsub.Event[session.Session]{Type: pubsub.UpdatedEvent, Payload: session.Session{ID: "s1"}},
	}
	batch := []pubsub.Event[tea.Msg]{
		msgUpdate(pubsub.UpdatedEvent, "m1", "draft 1"),
		sessEvent,
		msgUpdate(pubsub.UpdatedEvent, "m1", "draft 2"),
	}

	out := compactMessageUpdates(batch)
	require.Len(t, out, 2)
	require.IsType(t, pubsub.Event[session.Session]{}, out[0].Payload,
		"the session event must keep its position ahead of the surviving update")
	require.Equal(t, "draft 2", msgPayload(t, out[1]).Payload.Content().Text)
}

func TestCompactMessageUpdates_NoMessageEventsUnchanged(t *testing.T) {
	t.Parallel()

	batch := []pubsub.Event[tea.Msg]{
		{Type: pubsub.UpdatedEvent, Payload: "plain string msg"},
		{Type: pubsub.UpdatedEvent, Payload: 42},
	}
	out := compactMessageUpdates(batch)
	require.Len(t, out, 2)
}

func TestDrainEvents_DrainsOnlyWhatIsQueued(t *testing.T) {
	t.Parallel()

	ch := make(chan pubsub.Event[tea.Msg], 8)
	for i := range 5 {
		ch <- pubsub.Event[tea.Msg]{Type: pubsub.UpdatedEvent, Payload: i}
	}

	batch, closed := drainEvents(ch, 16)
	require.False(t, closed)
	require.Len(t, batch, 5)
	for i, ev := range batch {
		require.Equal(t, i, ev.Payload, "drained events must keep arrival order")
	}

	// The channel is now empty; a second drain returns immediately.
	batch, closed = drainEvents(ch, 16)
	require.False(t, closed)
	require.Empty(t, batch)
}

func TestDrainEvents_RespectsCap(t *testing.T) {
	t.Parallel()

	ch := make(chan pubsub.Event[tea.Msg], 8)
	for i := range 8 {
		ch <- pubsub.Event[tea.Msg]{Type: pubsub.UpdatedEvent, Payload: i}
	}

	batch, closed := drainEvents(ch, 3)
	require.False(t, closed)
	require.Len(t, batch, 3, "the drain must stop at the cap even with more queued")
}

// TestForwardEvents_CompactsBacklog drives the full TUI subscription
// path: a backlog of message updates queued while the consumer is
// blocked must collapse to the newest update per message before any
// of it is forwarded.
func TestForwardEvents_CompactsBacklog(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	app := &App{events: pubsub.NewBroker[tea.Msg]()}
	defer app.events.Shutdown()

	// The first send blocks until the test releases it, simulating a
	// UI busy rendering while more updates stream in behind it.
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	var got []tea.Msg
	send := func(msg tea.Msg) {
		once.Do(func() {
			close(entered)
			<-release
		})
		mu.Lock()
		got = append(got, msg)
		mu.Unlock()
	}

	go app.forwardEvents(ctx, send)
	require.Eventually(t, func() bool { return app.events.GetSubscriberCount() == 1 },
		2*time.Second, time.Millisecond, "forwardEvents must subscribe")

	app.events.Publish(pubsub.UpdatedEvent, msgUpdate(pubsub.UpdatedEvent, "m1", "draft 1").Payload)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("forwardEvents never forwarded the first event")
	}

	// The forward loop is now blocked in send; queue the backlog.
	for _, text := range []string{"draft 2", "draft 3", "draft 4"} {
		app.events.Publish(pubsub.UpdatedEvent, msgUpdate(pubsub.UpdatedEvent, "m1", text).Payload)
	}
	close(release)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) >= 2
	}, 2*time.Second, time.Millisecond, "the compacted backlog survivor must be forwarded")

	// Give any erroneously-uncompacted events a chance to arrive.
	time.Sleep(50 * time.Millisecond)
	cancel()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, got, 2,
		"the queued backlog must compact to a single survivor, got %d messages", len(got))
	e, ok := got[1].(pubsub.Event[message.Message])
	require.True(t, ok, "expected a message event, got %T", got[1])
	require.Equal(t, "draft 4", e.Payload.Content().Text,
		"the survivor must be the newest snapshot")
}

func TestDrainEvents_ReportsClosed(t *testing.T) {
	t.Parallel()

	ch := make(chan pubsub.Event[tea.Msg], 2)
	ch <- pubsub.Event[tea.Msg]{Type: pubsub.UpdatedEvent, Payload: "x"}
	close(ch)

	batch, closed := drainEvents(ch, 16)
	require.True(t, closed, "a closed channel must be reported")
	require.Len(t, batch, 1, "queued events ahead of the close must still drain")
}
