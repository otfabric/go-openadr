// SPDX-License-Identifier: MIT

package client

import (
	"context"
	"fmt"
	"iter"
	"reflect"

	openadr "github.com/otfabric/go-openadr"
)

// Each iterates over a whole search result, fetching it page by page with
// search. It stops at the first error, which it yields with a zero value.
//
//	for event, err := range client.Each(ctx, func(ctx context.Context, p client.Page) ([]openadr.Event, error) {
//		return ven.Events(ctx, client.EventQuery{ProgramID: id, Page: p})
//	}) {
//		...
//	}
//
// The result is not a snapshot: objects created or deleted while iterating
// may be skipped or seen twice.
//
// A full page that is the same as the one before it means a VTN that does
// not honour skip: Each then yields an error that matches
// openadr.ErrProtocol instead of asking for the same page for ever. A VTN
// that keeps returning full pages of new objects is not told from a long
// result: bound it with ctx.
func Each[T any](ctx context.Context, search func(context.Context, Page) ([]T, error)) iter.Seq2[T, error] {
	// The largest page a VTN returns.
	const pageSize = 50
	return func(yield func(T, error) bool) {
		var previous []T
		for skip := 0; ; skip += pageSize {
			items, err := search(ctx, Page{Skip: skip, Limit: pageSize})
			if err == nil && len(items) >= pageSize && reflect.DeepEqual(items, previous) {
				err = fmt.Errorf("%w: the page at skip %d is the page before it again; the VTN does not honour skip", openadr.ErrProtocol, skip)
			}
			if err != nil {
				var zero T
				yield(zero, err)
				return
			}
			previous = items
			for _, item := range items {
				if !yield(item, nil) {
					return
				}
			}
			if len(items) < pageSize {
				return
			}
		}
	}
}
