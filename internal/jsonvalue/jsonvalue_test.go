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

func TestLookupAs(t *testing.T) {
	t.Parallel()
	value, err := jsonvalue.ParseObject([]byte(`{"name":"serpe","ok":true,"n":7}`), jsonvalue.ObjectLimits())
	if err != nil {
		t.Fatal(err)
	}
	name, present, err := value.LookupAs[string]("name")
	if err != nil || !present || name != "serpe" {
		t.Fatalf("string LookupAs = %q %v %v", name, present, err)
	}
	ok, present, err := value.LookupAs[bool]("ok")
	if err != nil || !present || !ok {
		t.Fatalf("bool LookupAs = %v %v %v", ok, present, err)
	}
	n, present, err := value.LookupAs[int64]("n")
	if err != nil || !present || n != 7 {
		t.Fatalf("int64 LookupAs = %d %v %v", n, present, err)
	}
	_, present, err = value.LookupAs[string]("missing")
	if present || err != nil {
		t.Fatalf("missing LookupAs present=%v err=%v", present, err)
	}
	_, present, err = value.LookupAs[string]("n")
	if !present || err == nil {
		t.Fatal("type mismatch should be present with error")
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
