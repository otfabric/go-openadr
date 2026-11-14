// SPDX-License-Identifier: MIT

//go:build race

package codecs

// raceFactor is how much more the race detector allocates, at most, for
// what TestNotificationAllocationBudget decodes.
const raceFactor = 1.75
