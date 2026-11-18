package review

import (
	"github.com/hea3ven/orpheus/internal/work"
	"time"
)

// Reservation retains the complete selected review contract and scope.
type Reservation struct {
	ExpectedVersion *work.StateVersion
	Pipeline        string
	Contract        work.ReviewContract
	StartedAt       time.Time
	Scope           work.ReviewScope
}
