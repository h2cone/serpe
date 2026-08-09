package shared

import "github.com/h2cone/serpe/core/models"

// EquivalentContent reports whether two normalized content sequences carry the
// same meaning. Tool arguments are compared as JSON values so insignificant
// whitespace and object-key order do not break provider-state round trips.
func EquivalentContent(left, right []models.Content) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !left[index].Equal(right[index]) {
			return false
		}
	}
	return true
}
