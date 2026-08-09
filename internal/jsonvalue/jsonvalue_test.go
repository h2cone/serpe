package jsonvalue_test

import (
	"bytes"
	"testing"

	"github.com/h2cone/serpe/internal/jsonvalue"
)

func TestCanonicalValueSemantics(t *testing.T) {
	t.Parallel()
	left, err := jsonvalue.Canonical([]byte(`{"b":1,"a":{"y":2,"x":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	right, err := jsonvalue.Canonical([]byte(` { "a": { "x": true, "y": 2 }, "b": 1 } `))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(left, right) || !jsonvalue.Equal(left, right) {
		t.Fatalf("canonical values differ: %s != %s", left, right)
	}
	if jsonvalue.Equal([]byte(`1`), []byte(`1.0`)) {
		t.Fatal("different number lexemes must remain distinct")
	}
}

func TestCanonicalObject(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		raw  string
		want bool
	}{
		{raw: ` {"x":1} `, want: true},
		{raw: `[]`},
		{raw: `{`},
		{raw: `null`},
		{raw: `{} []`},
	} {
		if got := jsonvalue.IsObject([]byte(test.raw)); got != test.want {
			t.Errorf("IsObject(%q)=%v want=%v", test.raw, got, test.want)
		}
	}
}
