package logbuf

import (
	"encoding/json"
	"fmt"
)

// EventsKind is the spool kind (directory name) of click events.
const EventsKind = "events"

// encodeEvents serializes a batch as the JSON array stored in a segment.
// JSON is chosen over a positional or schema-bound encoding on purpose: a field
// added to Event later reads as its zero value from an older segment, a field
// removed is skipped, and a longer or shorter Subs array does not refuse the
// segment. Event fields must therefore never be renamed or change their type.
// It returns the marshal error.
func encodeEvents(batch []Event) ([]byte, error) {
	return json.Marshal(batch)
}

// decodeEvents parses the payload written by encodeEvents.
// It returns an error when the payload is not a JSON array of events or holds a
// different number of events than the segment header declared.
func decodeEvents(payload []byte, want int) ([]Event, error) {
	var batch []Event
	if err := json.Unmarshal(payload, &batch); err != nil {
		return nil, fmt.Errorf("events payload: %w", err)
	}
	if len(batch) != want {
		return nil, fmt.Errorf("events payload holds %d events, the header says %d", len(batch), want)
	}
	return batch, nil
}
