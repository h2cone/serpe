package core_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/h2cone/ouro/core"
	"github.com/h2cone/ouro/core/models"
)

func TestModelAliasRegistry(t *testing.T) {
	modelAlias := t.Name()
	upstreamModel := fakeUpstreamModel{}
	if err := core.RegisterModel(modelAlias, upstreamModel); err != nil {
		t.Fatalf("RegisterModel: %v", err)
	}
	t.Cleanup(func() { core.UnregisterModel(modelAlias) })
	if err := core.RegisterModel(modelAlias, upstreamModel); !errors.Is(err, core.ErrModelAlreadyRegistered) {
		t.Fatalf("duplicate RegisterModel error = %v", err)
	}
	resolvedUpstreamModel, err := core.Model(modelAlias)
	if err != nil || resolvedUpstreamModel == nil {
		t.Fatalf("Model() = %#v, %v", resolvedUpstreamModel, err)
	}
	var wait sync.WaitGroup
	for range 20 {
		wait.Go(func() {
			if got, lookupErr := core.Model(modelAlias); lookupErr != nil || got == nil {
				t.Errorf("concurrent Model() = %#v, %v", got, lookupErr)
			}
		})
	}
	wait.Wait()
}

func TestModelAliasUnknown(t *testing.T) {
	t.Parallel()
	if _, err := core.Model("missing-" + t.Name()); !errors.Is(err, core.ErrModelNotFound) {
		t.Fatalf("Model error = %v", err)
	}
}

type fakeUpstreamModel struct{}

func (fakeUpstreamModel) Complete(context.Context, *models.Request) (*models.Response, error) {
	return nil, fmt.Errorf("unused")
}

func (fakeUpstreamModel) Stream(context.Context, *models.Request) (models.Stream, error) {
	return nil, fmt.Errorf("unused")
}
