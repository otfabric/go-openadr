// SPDX-License-Identifier: MIT

package client

import (
	"context"
	"iter"
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
func Each[T any](ctx context.Context, search func(context.Context, Page) ([]T, error)) iter.Seq2[T, error] {
	// The largest page a VTN returns.
	const pageSize = 50
	return func(yield func(T, error) bool) {
		for skip := 0; ; skip += pageSize {
			items, err := search(ctx, Page{Skip: skip, Limit: pageSize})
			if err != nil {
				var zero T
				yield(zero, err)
				return
			}
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
