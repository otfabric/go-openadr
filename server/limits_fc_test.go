// SPDX-License-Identifier: MIT

package server

import (
	"testing"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/server/storage"
)

func TestDeliveryLimits(t *testing.T) {
	for in, want := range map[[2]int][2]int{
		{0, 0}:   {defaultMaxDeliveries, defaultMaxDeliveryBytes},
		{-1, -1}: {0, 0},
		{7, 9}:   {7, 9},
	} {
		l := newLimits(Limits{Deliveries: in[0], DeliveryBytes: in[1]})
		if got := [2]int{l.deliveries, l.deliveryBytes}; got != want {
			t.Errorf("Limits{Deliveries: %d, DeliveryBytes: %d}: %v, want %v", in[0], in[1], got, want)
		}
	}
}

func TestWebhookToken(t *testing.T) {
	rec := storage.SubscriptionRecord{Subscription: openadr.Subscription{ObjectOperations: []openadr.ObjectOperation{
		{Mechanism: openadr.MechanismWebSocket},
		{CallbackURL: "https://example.com/a", BearerToken: "t"},
	}}}
	if token, ok := webhookToken(rec, "https://example.com/a"); !ok || token != "t" {
		t.Errorf("a named URL: %q, %v", token, ok)
	}
	for _, url := range []string{"https://example.com/b", ""} {
		if _, ok := webhookToken(rec, url); ok {
			t.Errorf("%q is taken for a URL the subscription names", url)
		}
	}
}
