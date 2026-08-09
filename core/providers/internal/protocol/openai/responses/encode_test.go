package responses

import (
	"errors"
	"testing"

	"github.com/h2cone/serpe/core/models"
)

func TestMarshalJSONNormalizesError(t *testing.T) {
	t.Parallel()
	_, err := marshalJSON(make(chan int))
	var modelErr *models.Error
	if !errors.As(err, &modelErr) || modelErr.Kind != models.ErrorInvalidRequest || modelErr.Code != "encode_json" {
		t.Fatalf("marshalJSON error = %#v", err)
	}
}
