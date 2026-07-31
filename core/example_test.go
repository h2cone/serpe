package core_test

import (
	"context"
	"fmt"

	"github.com/h2cone/ouro/core"
	"github.com/h2cone/ouro/core/models"
)

func ExampleModel() {
	logical := logicalExampleModel{}
	if err := core.RegisterModel("assistant-example", logical); err != nil {
		panic(err)
	}
	defer core.UnregisterModel("assistant-example")

	model, err := core.Model("assistant-example")
	if err != nil {
		panic(err)
	}
	// Local and cloud callers receive the same small models.Model abstraction.
	response, err := model.Complete(context.Background(), models.NewTextRequest("hello"))
	if err != nil {
		panic(err)
	}
	fmt.Println(response.Text())
	// Output: logical hello
}

type logicalExampleModel struct{}

func (logicalExampleModel) Complete(context.Context, *models.Request) (*models.Response, error) {
	return &models.Response{
		Provider: "example",
		Status:   models.ResponseStatusCompleted,
		Candidates: []models.Candidate{{
			Index:        0,
			Content:      []models.Content{models.Text("logical hello")},
			FinishReason: models.FinishStop,
		}},
	}, nil
}

func (logicalExampleModel) Stream(context.Context, *models.Request) (models.Stream, error) {
	return nil, fmt.Errorf("stream is not used by this example")
}
