package models

import "fmt"

// validateEventShape rejects payload combinations that do not belong to the
// tagged EventKind. Ordering and active-part checks remain in reducer.
func validateEventShape(event Event) error {
	switch event.Kind {
	case EventResponseStart:
		if event.Response == nil {
			return fmt.Errorf("response_start is missing response metadata")
		}
		if event.CandidateIndex != 0 || event.PartIndex != 0 || contentSet(event.Part) || deltaSet(event.Delta) || len(event.Finishes) != 0 || event.Usage != nil || event.ItemID != "" || event.CallID != "" {
			return fmt.Errorf("response_start contains part or terminal payload")
		}
	case EventPartStart:
		if err := partPayloadShape(event); err != nil {
			return err
		}
		if deltaSet(event.Delta) {
			return fmt.Errorf("part_start contains a delta")
		}
		if err := validatePartStart(event.Part); err != nil {
			return err
		}
	case EventPartDelta:
		if err := partPayloadShape(event); err != nil {
			return err
		}
		if contentSet(event.Part) {
			return fmt.Errorf("part_delta contains a part")
		}
		if err := validateDeltaShape(event.Delta); err != nil {
			return err
		}
	case EventPartEnd:
		if err := partPayloadShape(event); err != nil {
			return err
		}
		if contentSet(event.Part) || deltaSet(event.Delta) {
			return fmt.Errorf("part_end contains part data")
		}
	case EventResponseEnd:
		if event.Response == nil {
			return fmt.Errorf("response_end is missing response metadata")
		}
		if event.CandidateIndex != 0 || event.PartIndex != 0 || contentSet(event.Part) || deltaSet(event.Delta) || event.ItemID != "" || event.CallID != "" {
			return fmt.Errorf("response_end contains part payload")
		}
		for i := range event.Finishes {
			if event.Finishes[i].CandidateIndex < 0 {
				return fmt.Errorf("response_end finish %d has a negative candidate index", i)
			}
		}
	default:
		return fmt.Errorf("unknown event kind %q", event.Kind)
	}
	return nil
}

func partPayloadShape(event Event) error {
	if event.CandidateIndex < 0 || event.PartIndex < 0 {
		return fmt.Errorf("part indexes must be non-negative")
	}
	if event.Response != nil || len(event.Finishes) != 0 || event.Usage != nil {
		return fmt.Errorf("part event contains response payload")
	}
	return nil
}

func validateDeltaShape(delta Delta) error {
	switch delta.Kind {
	case DeltaText, DeltaToolArguments, DeltaReasoningSummary, DeltaRefusal:
		if len(delta.Media) != 0 {
			return fmt.Errorf("textual delta contains media bytes")
		}
	case DeltaMediaBytes:
		if delta.Text != "" {
			return fmt.Errorf("media delta contains text")
		}
	default:
		return fmt.Errorf("unknown delta kind %q", delta.Kind)
	}
	return nil
}

func contentSet(content Content) bool {
	return content.Kind != "" || unionCount(content) != 0
}

func deltaSet(delta Delta) bool {
	return delta.Kind != "" || delta.Text != "" || len(delta.Media) != 0
}
