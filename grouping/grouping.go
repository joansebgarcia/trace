// Package grouping computes the Group Key that decides which Issue an Event
// belongs to.
package grouping

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/joansgarcia/trace/event"
)

// Key returns the Group Key of event as a hex SHA-256. This provisional
// version takes the type of each exception and the function and filename of
// its In-app frames, as sent, so line, column and message never split an
// Issue.
func Key(event event.Event) string {
	var components []string
	for _, exception := range event.Exceptions {
		components = append(components, exception.Type)
		for _, frame := range exception.Frames {
			if frame.IsInApp() {
				components = append(components, frame.Function, frame.Filename)
			}
		}
	}

	// A JSON array keeps the components apart, whatever they contain.
	encoded, _ := json.Marshal(components)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
