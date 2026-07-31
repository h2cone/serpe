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

func TestLogicalModelRegistry(t *testing.T) {
	name := t.Name()
	fake := fakeModel{}
	if err := core.RegisterModel(name, fake); err != nil {
		t.Fatalf("RegisterModel: %v", err)
	}
	t.Cleanup(func() { core.UnregisterModel(name) })
	if err := core.RegisterModel(name, fake); !errors.Is(err, core.ErrModelAlreadyRegistered) {
		t.Fatalf("duplicate RegisterModel error = %v", err)
	}
	model, err := core.Model(name)
	if err != nil || model == nil {
		t.Fatalf("Model() = %#v, %v", model, err)
	}
	var wait sync.WaitGroup
	for range 20 {
		wait.Go(func() {
			if got, lookupErr := core.Model(name); lookupErr != nil || got == nil {
				t.Errorf("concurrent Model() = %#v, %v", got, lookupErr)
			}
		})
	}
	wait.Wait()
}

func TestLogicalModelUnknown(t *testing.T) {
	t.Parallel()
	if _, err := core.Model("missing-" + t.Name()); !errors.Is(err, core.ErrModelNotFound) {
		t.Fatalf("Model error = %v", err)
	}
}

type fakeModel struct{}

func (fakeModel) Complete(context.Context, *models.Request) (*models.Response, error) {
	return nil, fmt.Errorf("unused")
}

func (fakeModel) Stream(context.Context, *models.Request) (models.Stream, error) {
	return nil, fmt.Errorf("unused")
}
