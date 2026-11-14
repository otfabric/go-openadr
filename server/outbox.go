// SPDX-License-Identifier: MIT

package server

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/server/storage"
)

// ConfirmingSender is a WebhookSender that says what became of a
// notification, which is what it takes to keep notifications until they
// are delivered (Config.Outbox). *webhook.Sender implements it.
type ConfirmingSender interface {
	WebhookSender
	// SendConfirmed is Send, and calls done exactly once: with nil when
	// the webhook confirmed the notification, and with an error when the
	// sender has given up on it. An error that has a method
	// Permanent() bool which returns true says that trying again is of no
	// use; any other, that it may be.
	SendConfirmed(callbackURL, bearerToken string, body []byte, done func(error))
}

// outbox is the part of a VTN that keeps webhook notifications until they
// are delivered.
//
// A notification is recorded before it is handed to the sender, and
// forgotten when the webhook has confirmed it. Whatever is still recorded
// is handed to the sender again: when the VTN starts, which is what makes
// a notification survive a restart, and every so often while it runs,
// which is what makes it survive a webhook that was down for longer than
// the sender's attempts last. Until it is too old to be of use.
//
// The notifications of one callback URL are delivered one at a time and
// oldest first: the next is not handed over before the one before it has
// been confirmed, or refused for good. So their order holds across
// failures and restarts, and a webhook that is down holds up its own
// notifications and nobody else's.
//
// Delivery is at least once: a webhook may see a notification twice, when
// its confirmation was lost with the process.
type outbox struct {
	s      *Server
	store  storage.Outbox
	sender ConfirmingSender
	ttl    time.Duration
	every  time.Duration

	mu sync.Mutex
	// busy holds the callback URLs that have a notification with the
	// sender, and more says that one of them was given another meanwhile.
	busy     map[string]bool
	more     map[string]bool
	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
}

func newOutbox(s *Server, store storage.Outbox, sender ConfirmingSender, ttl, every time.Duration) *outbox {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	if every <= 0 {
		every = time.Minute
	}
	o := &outbox{s: s, store: store, sender: sender, ttl: ttl, every: every,
		busy: map[string]bool{}, more: map[string]bool{}, stop: make(chan struct{}), done: make(chan struct{})}
	go o.run()
	return o
}

func (o *outbox) warn(msg string, d storage.Delivery, err error) {
	o.s.logger.Warn(msg, slog.String("delivery", d.ID), slog.String("subscription", d.SubscriptionID), slog.Any("error", err))
}

// call calls the application's code that callback names: the Outbox, the
// Store, the sender or the clock. It reports false when that did not
// return normally, which is logged: the caller goes on as after an error,
// and the notification stays recorded for the loop to take up again.
func (o *outbox) call(callback string, f func()) bool {
	return o.s.logger.Call(callback, f)
}

// add records a notification and sees to its delivery.
func (o *outbox) add(ctx context.Context, subscriptionID, callbackURL, bearerToken string, body []byte) {
	meta, err := o.s.created()
	d := storage.Delivery{ID: meta.ID, SubscriptionID: subscriptionID, CallbackURL: callbackURL, BearerToken: bearerToken, Body: body, Created: meta.Created}
	if err == nil {
		err = o.store.AddDelivery(ctx, d)
	}
	if err != nil {
		// It cannot be kept: it is sent all the same, once, as a VTN
		// without an outbox would.
		o.warn("openadr: a notification could not be recorded and will not survive a restart", d, err)
		o.sender.Send(callbackURL, bearerToken, body)
		return
	}
	o.pump(callbackURL)
}

// pump starts delivering what is recorded for a callback URL, unless that
// is going on already.
func (o *outbox) pump(callbackURL string) {
	o.mu.Lock()
	if o.busy[callbackURL] {
		// Whoever is at it is told to look again when it is through.
		o.more[callbackURL] = true
		o.mu.Unlock()
		return
	}
	o.busy[callbackURL] = true
	o.mu.Unlock()
	o.next(callbackURL, nil)
}

// rest ends the delivering for a callback URL, unless more came in.
func (o *outbox) rest(callbackURL string) (resting bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.more[callbackURL] {
		delete(o.more, callbackURL)
		return false
	}
	delete(o.busy, callbackURL)
	return true
}

// release ends the delivering for a callback URL after a failure: the
// loop takes it up again.
func (o *outbox) release(callbackURL string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.busy, callbackURL)
	delete(o.more, callbackURL)
}

// next hands the oldest notification of a callback URL to the sender and
// arranges for the one after it to follow. queue is what is known to be
// recorded for the URL, oldest first; when it is empty the store is asked.
func (o *outbox) next(callbackURL string, queue []storage.Delivery) {
	// Without the request that caused any of it: that is long answered.
	ctx := context.Background()
	for {
		select {
		case <-o.stop:
			o.release(callbackURL)
			return
		default:
		}
		if len(queue) == 0 {
			var all []storage.Delivery
			var err error
			if !o.call("Outbox.Deliveries", func() { all, err = o.store.Deliveries(ctx) }) {
				o.release(callbackURL)
				return
			}
			if err != nil {
				o.s.logger.Warn("openadr: the recorded notifications could not be read", slog.Any("error", err))
				o.release(callbackURL)
				return
			}
			for _, d := range all {
				if d.CallbackURL == callbackURL {
					queue = append(queue, d)
				}
			}
			if len(queue) == 0 {
				if o.rest(callbackURL) {
					return
				}
				continue
			}
		}
		d := queue[0]
		queue = queue[1:]

		var old bool
		if !o.call("Config.Now", func() { old = o.s.now().Sub(d.Created) > o.ttl }) {
			o.release(callbackURL)
			return
		}
		give := ""
		if old {
			give = "openadr: a notification is too old to be of use and is given up"
		} else {
			var err error
			if !o.call("Store.Subscriptions", func() { _, err = o.s.store.Subscriptions().Get(ctx, d.SubscriptionID) }) {
				o.release(callbackURL)
				return
			}
			if errors.Is(err, openadr.ErrNotFound) {
				// Nobody is waiting for it any more.
				give = "openadr: the subscription of a notification is gone and it is given up"
			}
		}
		if give != "" {
			o.warn(give, d, nil)
			if !o.call("Outbox.RemoveDelivery", func() { _ = o.store.RemoveDelivery(ctx, d.ID) }) {
				o.release(callbackURL)
				return
			}
			continue
		}

		rest := queue
		// settled is taken by whichever comes first: the sender's answer,
		// or a SendConfirmed that did not return normally. The other is
		// ignored, so that the URL has one delivery on its way at most.
		var settled atomic.Bool
		sent := o.call("ConfirmingSender.SendConfirmed", func() {
			o.sender.SendConfirmed(d.CallbackURL, d.BearerToken, d.Body, func(err error) {
				if settled.CompareAndSwap(false, true) {
					o.confirmed(ctx, callbackURL, d, rest, err)
				}
			})
		})
		if !sent && settled.CompareAndSwap(false, true) {
			o.release(callbackURL)
		}
		return
	}
}

// confirmed is what the sender said of d: forgotten when it was delivered
// or refused for good, and the next of the URL follows.
func (o *outbox) confirmed(ctx context.Context, callbackURL string, d storage.Delivery, rest []storage.Delivery, err error) {
	var refused interface{ Permanent() bool }
	final := errors.As(err, &refused) && refused.Permanent()
	if err != nil && !final {
		// It stays recorded, and so does what comes after it: the loop
		// takes the URL up again.
		o.release(callbackURL)
		return
	}
	if err != nil {
		o.warn("openadr: a notification was refused for good and is given up", d, err)
	}
	var rerr error
	if !o.call("Outbox.RemoveDelivery", func() { rerr = o.store.RemoveDelivery(ctx, d.ID) }) {
		o.release(callbackURL)
		return
	}
	if rerr != nil {
		o.warn("openadr: a delivered notification could not be forgotten and may be sent again", d, rerr)
		o.release(callbackURL)
		return
	}
	o.next(callbackURL, rest)
}

// run takes up what is recorded and not being delivered: at once, for what
// a VTN before this one left behind, and then every so often.
func (o *outbox) run() {
	defer close(o.done)
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-o.stop:
			return
		case <-timer.C:
		}
		var pending []storage.Delivery
		var err error
		o.call("Outbox.Deliveries", func() { pending, err = o.store.Deliveries(context.Background()) })
		if err != nil {
			o.s.logger.Warn("openadr: the recorded notifications could not be read", slog.Any("error", err))
		}
		seen := map[string]bool{}
		for _, d := range pending {
			if !seen[d.CallbackURL] {
				seen[d.CallbackURL] = true
				o.pump(d.CallbackURL)
			}
		}
		timer.Reset(o.every)
	}
}

// close stops the loop and waits for it. It may be called more than once
// and from several goroutines at the same time.
func (o *outbox) close() {
	o.stopOnce.Do(func() { close(o.stop) })
	<-o.done
}
