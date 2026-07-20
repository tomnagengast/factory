package eventwire

import (
	"encoding/json"
	"time"
)

// Event is the only durable fact in Factory. Its ordered ID is also used as
// the integer ID for an entity created by the event.
type Event struct {
	ID   int64           `json:"id"`
	Type string          `json:"type"`
	At   time.Time       `json:"at"`
	Data json.RawMessage `json:"data"`
}
