package shared

// FirstNonempty returns the first non-empty string.
func FirstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
