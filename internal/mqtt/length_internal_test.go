// SPDX-License-Identifier: MIT

package mqtt

import (
	"errors"
	"testing"
)

func TestCheckLength(t *testing.T) {
	if err := checkLength(MaxRemaining); err != nil {
		t.Errorf("MaxRemaining: %v", err)
	}
	if err := checkLength(MaxRemaining + 1); !errors.Is(err, ErrTooLong) {
		t.Errorf("MaxRemaining+1: %v", err)
	}
}
