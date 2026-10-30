// SPDX-License-Identifier: MIT

package openadr

import "testing"

// BenchmarkParseDuration measures the ISO 8601 duration parser.
func BenchmarkParseDuration(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := ParseDuration("P1DT2H30M"); err != nil {
			b.Fatal(err)
		}
	}
}
