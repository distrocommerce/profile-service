package httpapi

import (
	"strings"
	"testing"
)

func TestValidateDisplayName(t *testing.T) {
	ok := []struct{ name, in, want string }{
		{"empty", "", ""},
		{"whitespace only", " \t\n ", ""},
		{"64 two-byte runes", strings.Repeat("é", 64), strings.Repeat("é", 64)},
		{"64 emoji", strings.Repeat("😀", 64), strings.Repeat("😀", 64)},
		{"64 runes with surrounding spaces", "  " + strings.Repeat("a", 64) + "  ", strings.Repeat("a", 64)},
		{"trailing newline is trimmed", "Ada\n", "Ada"},
		{"surrounding NBSP and NEL are trimmed", " Ada\u0085", "Ada"},
		{"ZWJ emoji sequence", "👩‍💻 Ada", "👩‍💻 Ada"},
		{"non-Latin scripts", "Ада 艾达 أدا", "Ада 艾达 أدا"},
		{"inner spaces kept", "Ada  Lovelace", "Ada  Lovelace"},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			got, ferr := validateDisplayName(tc.in)
			if ferr != nil || got != tc.want {
				t.Errorf("validateDisplayName(%q) = %q, %+v; want %q", tc.in, got, ferr, tc.want)
			}
		})
	}

	bad := []struct{ name, in, code string }{
		{"65 runes", strings.Repeat("a", 65), fieldTooLong},
		{"65 emoji", strings.Repeat("😀", 65), fieldTooLong},
		{"inner newline", "Ada\nLovelace", fieldInvalidCharacter},
		{"inner tab", "Ada\tL", fieldInvalidCharacter},
		{"inner NUL", "Ada\x00L", fieldInvalidCharacter},
		{"inner DEL", "Ada\x7fL", fieldInvalidCharacter},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, ferr := validateDisplayName(tc.in)
			if ferr == nil || ferr.Code != tc.code || ferr.Field != "display_name" || ferr.Detail == "" {
				t.Errorf("validateDisplayName(%q) error = %+v, want code %q", tc.in, ferr, tc.code)
			}
		})
	}
}
