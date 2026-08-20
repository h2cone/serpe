package shared

import (
	"bytes"
	"testing"
)

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

func TestEncodeJSONIsDeterministicAndStrict(t *testing.T) {
	t.Parallel()
	encoded, err := EncodeJSON(map[string]int{"b": 2, "a": 1})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, []byte(`{"a":1,"b":2}`)) {
		t.Fatalf("EncodeJSON = %s", encoded)
	}
	var got map[string]int
	if err := DecodeJSON(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if err := DecodeJSON([]byte(`{"a":1,"a":2}`), &got); err == nil {
		t.Fatal("duplicate names should be rejected")
	}
}
