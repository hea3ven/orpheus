package review_test

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Review checks inherit the alternate reviewer selected for the outer
	// Orpheus run. Keep unrelated tests independent from that ambient setting;
	// paired-review tests opt in explicitly with t.Setenv.
	if err := os.Unsetenv("ORPHEUS_ALTERNATE_REVIEWER_PROFILE"); err != nil {
		panic(fmt.Sprintf("clear alternate reviewer environment: %v", err))
	}
	os.Exit(m.Run())
}
