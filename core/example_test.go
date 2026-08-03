package core_test

import (
	"context"
	"fmt"

	"github.com/h2cone/ouro/core"
	"github.com/h2cone/ouro/core/models"
)

func ExampleModel() {
	const modelAlias = "assistant-example"
	upstreamModel := upstreamExampleModel{}
	if err := core.RegisterModel(modelAlias, upstreamModel); err != nil {
		panic(err)
	}
	defer core.UnregisterModel(modelAlias)

	resolvedUpstreamModel, err := core.Model(modelAlias)
	if err != nil {
		panic(err)
	}
	// Local and cloud callers receive the same small models.Model abstraction.
	response, err := resolvedUpstreamModel.Complete(context.Background(), models.NewTextRequest("hello"))
	if err != nil {
		panic(err)
	}
	fmt.Println(response.Text())
	// Output: upstream hello
}

type upstreamExampleModel struct{}

func (upstreamExampleModel) Complete(context.Context, *models.Request) (*models.Response, error) {
	return &models.Response{
		Provider: "example",
		Status:   models.ResponseStatusCompleted,
		Candidates: []models.Candidate{{
			Index:        0,
			Content:      []models.Content{models.Text("upstream hello")},
			FinishReason: models.FinishStop,
		}},
	}, nil
}

func (upstreamExampleModel) Stream(context.Context, *models.Request) (models.Stream, error) {
	return nil, fmt.Errorf("stream is not used by this example")
}
