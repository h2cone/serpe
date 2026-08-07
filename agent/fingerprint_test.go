package agent

import (
	"encoding/json"
	"testing"

	"github.com/h2cone/ouro/core/models"
)

func TestCanonicalJSONObjectKeyOrder(t *testing.T) {
	t.Parallel()
	a, err := canonicalJSONObject(json.RawMessage(`{"b":1,"a":2}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := canonicalJSONObject(json.RawMessage(`{ "a" : 2 , "b" : 1 }`))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("canonical mismatch: %q vs %q", a, b)
	}
}

func TestStepFingerprintIgnoresCallID(t *testing.T) {
	t.Parallel()
	calls1 := []models.ToolCall{{ID: "1", Name: "now", Arguments: json.RawMessage(`{"x":1}`)}}
	calls2 := []models.ToolCall{{ID: "2", Name: "now", Arguments: json.RawMessage(`{"x":1}`)}}
	results := []ToolOutput{TextResult("ok")}
	fp1, err := stepFingerprint(calls1, results)
	if err != nil {
		t.Fatal(err)
	}
	fp2, err := stepFingerprint(calls2, results)
	if err != nil {
		t.Fatal(err)
	}
	if fp1 != fp2 {
		t.Fatalf("call ID should not affect fingerprint")
	}
}

func TestStepFingerprintResultChange(t *testing.T) {
	t.Parallel()
	calls := []models.ToolCall{{ID: "1", Name: "now", Arguments: json.RawMessage(`{}`)}}
	fp1, _ := stepFingerprint(calls, []ToolOutput{TextResult("a")})
	fp2, _ := stepFingerprint(calls, []ToolOutput{TextResult("b")})
	if fp1 == fp2 {
		t.Fatal("result change should change fingerprint")
	}
}

func TestStepFingerprintMultiCallOrder(t *testing.T) {
	t.Parallel()
	a := []models.ToolCall{
		{ID: "1", Name: "a", Arguments: json.RawMessage(`{}`)},
		{ID: "2", Name: "b", Arguments: json.RawMessage(`{}`)},
	}
	b := []models.ToolCall{
		{ID: "1", Name: "b", Arguments: json.RawMessage(`{}`)},
		{ID: "2", Name: "a", Arguments: json.RawMessage(`{}`)},
	}
	results := []ToolOutput{TextResult("x"), TextResult("x")}
	fp1, _ := stepFingerprint(a, results)
	fp2, _ := stepFingerprint(b, results)
	if fp1 == fp2 {
		t.Fatal("call order should matter")
	}
}

func TestContentFingerprintImageHash(t *testing.T) {
	t.Parallel()
	c1 := []models.Content{models.ImageBytes("image/png", []byte{1, 2})}
	c2 := []models.Content{models.ImageBytes("image/png", []byte{1, 3})}
	fp1, err := contentFingerprint(c1)
	if err != nil {
		t.Fatal(err)
	}
	fp2, err := contentFingerprint(c2)
	if err != nil {
		t.Fatal(err)
	}
	if fp1 == fp2 {
		t.Fatal("image bytes should affect hash")
	}
}

func TestContentFingerprintHasUnambiguousFraming(t *testing.T) {
	t.Parallel()
	a, err := contentFingerprint([]models.Content{models.Text("a\ntext:b")})
	if err != nil {
		t.Fatal(err)
	}
	b, err := contentFingerprint([]models.Content{models.Text("a"), models.Text("b")})
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("content block boundaries must affect fingerprint")
	}
	c, err := contentFingerprint([]models.Content{models.Text("b"), models.Text("a")})
	if err != nil {
		t.Fatal(err)
	}
	if b == c {
		t.Fatal("content order must affect fingerprint")
	}
}

func TestContentFingerprintIncludesImageSemantics(t *testing.T) {
	t.Parallel()
	uriA := models.ImageURI("https://example.com/a.png")
	uriB := models.ImageURI("https://example.com/b.png")
	mimeA := models.ImageBytes("image/png", []byte{1})
	mimeB := models.ImageBytes("image/jpeg", []byte{1})
	detailA := models.ImageURI("https://example.com/a.png")
	detailA.Image.Detail = models.ImageDetailLow
	detailB := models.ImageURI("https://example.com/a.png")
	detailB.Image.Detail = models.ImageDetailHigh

	for name, pair := range map[string][2]models.Content{
		"uri":    {uriA, uriB},
		"mime":   {mimeA, mimeB},
		"detail": {detailA, detailB},
	} {
		pair := pair
		t.Run(name, func(t *testing.T) {
			a, err := contentFingerprint([]models.Content{pair[0]})
			if err != nil {
				t.Fatal(err)
			}
			b, err := contentFingerprint([]models.Content{pair[1]})
			if err != nil {
				t.Fatal(err)
			}
			if a == b {
				t.Fatalf("%s must affect fingerprint", name)
			}
		})
	}
}

func TestStepFingerprintRejectsInvalidArgumentsAndShape(t *testing.T) {
	t.Parallel()
	if _, err := stepFingerprint(
		[]models.ToolCall{{ID: "1", Name: "f", Arguments: json.RawMessage(`[]`)}},
		[]ToolOutput{TextResult("ok")},
	); err == nil {
		t.Fatal("non-object arguments must fail")
	}
	if _, err := stepFingerprint(
		[]models.ToolCall{{ID: "1", Name: "f", Arguments: json.RawMessage(`{}`)}},
		nil,
	); err == nil {
		t.Fatal("call/result count mismatch must fail")
	}
}
