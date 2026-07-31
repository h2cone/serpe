package shared

import "github.com/h2cone/ouro/core/models"

// EventQueue is a single-reader FIFO for normalized stream events. Shift is
// O(1) and clears consumed entries so large event payloads are not retained for
// the lifetime of a stream.
type EventQueue struct {
	events []models.Event
	head   int
}

// Push appends events in delivery order.
func (q *EventQueue) Push(events ...models.Event) {
	q.events = append(q.events, events...)
}

// Shift removes and returns the oldest queued event.
func (q *EventQueue) Shift() (models.Event, bool) {
	if q.head == len(q.events) {
		return models.Event{}, false
	}
	event := q.events[q.head]
	q.events[q.head] = models.Event{}
	q.head++
	if q.head == len(q.events) {
		q.events = q.events[:0]
		q.head = 0
	}
	return event, true
}
