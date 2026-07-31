package daemon

import (
	"context"
	"sync"
	"time"

	"github.com/shreyashsri79/openair/internal/ipc"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// eventQueue is how many events a slow client may fall behind before it starts
// losing them. Events are advisory -- a device list, a progress bar -- and the
// authoritative state is always one request away, so dropping is the right
// failure. Blocking here would let a stalled UI stop the daemon.
const eventQueue = 64

// client is one connected shell: the CLI, or a tray UI.
type client struct {
	peer   *ipc.Peer
	events chan *openairv1.DaemonEvent

	mu      sync.Mutex
	subbed  bool
	prompts bool
	dropped uint64
}

func newClient(p *ipc.Peer) *client {
	return &client{peer: p, events: make(chan *openairv1.DaemonEvent, eventQueue)}
}

// subscribe turns this connection into an event sink, optionally one that can
// also answer prompts.
func (c *client) subscribe(prompts bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.subbed = true
	c.prompts = prompts
}

func (c *client) canPrompt() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.subbed && c.prompts
}

func (c *client) subscribed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.subbed
}

// pump writes queued events to the client until its connection ends.
func (c *client) pump(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.peer.Done():
			return
		case ev := <-c.events:
			if err := c.peer.Send(ipc.MsgEvent, ev); err != nil {
				return
			}
		}
	}
}

func (d *Daemon) addClient(c *client) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.clients[c] = struct{}{}
}

func (d *Daemon) removeClient(c *client) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.clients, c)
}

func (d *Daemon) clientList() []*client {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]*client, 0, len(d.clients))
	for c := range d.clients {
		out = append(out, c)
	}
	return out
}

// broadcast delivers an event to every subscribed client, dropping it for any
// client that has fallen a queue behind.
func (d *Daemon) broadcast(ev *openairv1.DaemonEvent) {
	for _, c := range d.clientList() {
		if !c.subscribed() {
			continue
		}
		select {
		case c.events <- ev:
		default:
			c.mu.Lock()
			c.dropped++
			c.mu.Unlock()
		}
	}
}

// ask puts a question to whoever is watching and returns the first answer.
//
// "First answer" rather than "unanimous" or "any approval": two open UIs are
// two views of one user, and asking them to agree would hang on whichever one
// nobody is looking at. A refusal from the first to answer is a refusal.
//
// No subscriber able to answer means refused. That is the whole reason
// AutoAccept exists as an explicit setting: a headless daemon must say so, not
// discover it by accepting something nobody saw.
func (d *Daemon) ask(ctx context.Context, prompt *openairv1.DaemonPrompt, timeout time.Duration) bool {
	var askable []*client
	for _, c := range d.clientList() {
		if c.canPrompt() {
			askable = append(askable, c)
		}
	}
	if len(askable) == 0 {
		d.cfg.Logf("refusing %s from %s: no client is watching",
			prompt.GetKind(), prompt.GetDeviceId())
		return false
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	answers := make(chan bool, len(askable))
	for _, c := range askable {
		go func(c *client) {
			// Each client gets its own copy: Ask stamps a request ID into the
			// message, and two goroutines stamping one message would have them
			// answer each other's questions.
			p := &openairv1.DaemonPrompt{
				Kind:        prompt.GetKind(),
				Text:        prompt.GetText(),
				DeviceId:    prompt.GetDeviceId(),
				DisplayName: prompt.GetDisplayName(),
				Platform:    prompt.GetPlatform(),
				Sas:         prompt.GetSas(),
				Files:       prompt.GetFiles(),
				TotalBytes:  prompt.GetTotalBytes(),
			}
			ok, err := c.peer.Ask(ctx, p)
			if err != nil {
				return
			}
			answers <- ok
		}(c)
	}

	select {
	case ok := <-answers:
		return ok
	case <-ctx.Done():
		d.cfg.Logf("refusing %s from %s: nobody answered within %s",
			prompt.GetKind(), prompt.GetDeviceId(), timeout)
		return false
	}
}
