package core

// FirstNonEmpty returns the first value that is not empty. It returns the value
// verbatim, without trimming. Callers that need trimmed values trim their
// inputs instead of relying on a second helper.
func FirstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
