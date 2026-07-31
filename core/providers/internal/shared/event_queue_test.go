package shared

import (
	"testing"

	"github.com/h2cone/ouro/core/models"
)

func TestEventQueueFIFOAndReuse(t *testing.T) {
	t.Parallel()
	var queue EventQueue
	queue.Push(
		models.Event{Kind: models.EventResponseStart},
		models.Event{Kind: models.EventPartStart},
	)
	for _, want := range []models.EventKind{models.EventResponseStart, models.EventPartStart} {
		event, ok := queue.Shift()
		if !ok || event.Kind != want {
			t.Fatalf("Shift() = %#v, %v; want kind %q", event, ok, want)
		}
	}
	if event, ok := queue.Shift(); ok {
		t.Fatalf("empty Shift() = %#v, true", event)
	}
	queue.Push(models.Event{Kind: models.EventResponseEnd})
	if event, ok := queue.Shift(); !ok || event.Kind != models.EventResponseEnd {
		t.Fatalf("reused Shift() = %#v, %v", event, ok)
	}
}
