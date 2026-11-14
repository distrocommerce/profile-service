package httpapi

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxDisplayNameLen is the display name limit in Unicode code points (not bytes).
const maxDisplayNameLen = 64

// validateDisplayName trims surrounding Unicode whitespace and checks the
// rest: at most maxDisplayNameLen code points and no control characters
// (category Cc). Empty means "no display name". Format characters (Cf, such
// as the ZWJ in emoji sequences) are allowed. It returns the trimmed name, or
// the field error.
func validateDisplayName(raw string) (string, *fieldError) {
	name := strings.TrimSpace(raw)
	if n := utf8.RuneCountInString(name); n > maxDisplayNameLen {
		return "", &fieldError{Field: "display_name", Code: fieldTooLong,
			Detail: fmt.Sprintf("must be at most %d characters", maxDisplayNameLen)}
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", &fieldError{Field: "display_name", Code: fieldInvalidCharacter,
			Detail: "must not contain control characters such as newlines or tabs"}
	}
	return name, nil
}
