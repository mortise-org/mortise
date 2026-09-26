package api

import "testing"

func TestEnvKeyDelta(t *testing.T) {
	cases := []struct {
		added, removed []string
		want           string
	}{
		{nil, nil, ""},
		{[]string{"B", "A"}, nil, " (added: A, B)"},
		{nil, []string{"OLD"}, " (removed: OLD)"},
		{[]string{"NEW"}, []string{"OLD"}, " (added: NEW; removed: OLD)"},
	}
	for _, c := range cases {
		if got := envKeyDelta(c.added, c.removed); got != c.want {
			t.Errorf("envKeyDelta(%v, %v) = %q, want %q", c.added, c.removed, got, c.want)
		}
	}
}
