package jsonvalue_test

import (
	"strings"
	"testing"

	"github.com/h2cone/serpe/internal/jsonvalue"
)

func TestParseObjectRejectsDuplicatesAndTrailing(t *testing.T) {
	t.Parallel()
	if _, err := jsonvalue.ParseObject([]byte(`{"a":1,"a":2}`), jsonvalue.ObjectLimits()); err == nil {
		t.Fatal("duplicate key")
	}
	if _, err := jsonvalue.ParseObject([]byte(`{"p":1,"\u0070":2}`), jsonvalue.ObjectLimits()); err == nil {
		t.Fatal("escaped duplicate key")
	}
	if _, err := jsonvalue.Parse([]byte(`{} []`), jsonvalue.ObjectLimits()); err == nil {
		t.Fatal("trailing value")
	}
	if _, err := jsonvalue.ParseObject([]byte(`[]`), jsonvalue.ObjectLimits()); err == nil {
		t.Fatal("array is not an object")
	}
}

func TestParseRejectsUTF8AndSurrogates(t *testing.T) {
	t.Parallel()
	if _, err := jsonvalue.Parse([]byte("\"\xff\""), jsonvalue.ObjectLimits()); err == nil {
		t.Fatal("illegal UTF-8")
	}
	if _, err := jsonvalue.Parse([]byte(`"\uD800"`), jsonvalue.ObjectLimits()); err == nil {
		t.Fatal("unpaired high surrogate")
	}
	v, err := jsonvalue.Parse([]byte(`"\uD83D\uDE00"`), jsonvalue.ObjectLimits())
	if err != nil {
		t.Fatal(err)
	}
	if v.String != "😀" {
		t.Fatalf("got %q", v.String)
	}
}

func TestParseNumberBudgets(t *testing.T) {
	t.Parallel()
	limits := jsonvalue.ObjectLimits()
	if _, err := jsonvalue.Parse([]byte("1"+strings.Repeat("0", 128)), limits); err == nil {
		t.Fatal("lexeme 129")
	}
	if _, err := jsonvalue.Parse([]byte("1e1001"), limits); err == nil {
		t.Fatal("exponent 1001")
	}
	if _, err := jsonvalue.Parse([]byte("1e1000"), limits); err != nil {
		t.Fatalf("exponent 1000: %v", err)
	}
	scaleOK := "1." + strings.Repeat("0", 24) + "e-1000"
	if _, err := jsonvalue.Parse([]byte(scaleOK), limits); err != nil {
		t.Fatalf("scale -1024: %v", err)
	}
	scaleBad := "1." + strings.Repeat("0", 25) + "e-1000"
	if _, err := jsonvalue.Parse([]byte(scaleBad), limits); err == nil {
		t.Fatal("scale -1025")
	}
}

func TestParseDepthAndNodes(t *testing.T) {
	t.Parallel()
	nested := strings.Repeat(`{"a":`, 129) + "1" + strings.Repeat(`}`, 129)
	if _, err := jsonvalue.Parse([]byte(nested), jsonvalue.Limits{MaxDepth: 128, MaxNodes: 1 << 20}); err == nil {
		t.Fatal("depth 129")
	}
	wide := `{"a":1,"b":2}`
	if _, err := jsonvalue.Parse([]byte(wide), jsonvalue.Limits{MaxNodes: 3}); err == nil {
		t.Fatal("nodes: object + 2 names + 2 numbers = 5")
	}
}

func TestCanonicalValueSortsKeys(t *testing.T) {
	t.Parallel()
	v, err := jsonvalue.Parse([]byte(`{"b":1,"a":{"y":2,"x":3}}`), jsonvalue.ObjectLimits())
	if err != nil {
		t.Fatal(err)
	}
	got, err := jsonvalue.CanonicalValue(v)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"a":{"x":3,"y":2},"b":1}` {
		t.Fatalf("got %s", got)
	}
}
