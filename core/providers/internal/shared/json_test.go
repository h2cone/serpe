package shared

import "testing"

func TestJSONObject(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		raw  string
		want bool
	}{
		{raw: ` {"x":1} `, want: true},
		{raw: `[]`},
		{raw: `{`},
		{raw: `null`},
	} {
		if got := JSONObject([]byte(test.raw)); got != test.want {
			t.Errorf("JSONObject(%q) = %v, want %v", test.raw, got, test.want)
		}
	}
}
