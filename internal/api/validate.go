package api

import "fmt"

// validateDNSLabel returns an error message if name is not a valid DNS-1123
// label with at most maxLen characters, or "" if it's acceptable.
func validateDNSLabel(field, name string, maxLen int) string {
	if name == "" {
		return field + " is required"
	}
	if len(name) > maxLen {
		return fmt.Sprintf("%s must be %d characters or fewer", field, maxLen)
	}
	if !dns1123LabelRegex.MatchString(name) {
		return field + " must be a DNS-1123 label: lowercase alphanumerics and '-', starting and ending with an alphanumeric"
	}
	return ""
}
