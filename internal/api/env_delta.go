package api

import (
	"sort"
	"strings"
)

// envKeyDelta renders added/removed key names for an activity message —
// names only, never values (CAI-350: a dropped key used to leave no trace).
// Empty when only values changed.
func envKeyDelta(added, removed []string) string {
	if len(added) == 0 && len(removed) == 0 {
		return ""
	}
	var parts []string
	if len(added) > 0 {
		sort.Strings(added)
		parts = append(parts, "added: "+strings.Join(added, ", "))
	}
	if len(removed) > 0 {
		sort.Strings(removed)
		parts = append(parts, "removed: "+strings.Join(removed, ", "))
	}
	return " (" + strings.Join(parts, "; ") + ")"
}
