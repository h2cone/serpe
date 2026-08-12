package sessions

import (
	"context"
	"sync"
	"testing"
)

func TestMemoryStoreConcurrent(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	ids := []string{"a", "b", "c", "d"}
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := ids[i%len(ids)]
			switch i % 4 {
			case 0:
				_ = store.Create(ctx, id, []byte("created"))
			case 1:
				_, _ = store.Load(ctx, id)
			case 2:
				_ = store.Delete(ctx, id)
			case 3:
				_ = store.Save(ctx, id, []byte("saved"))
			}
		}(i)
	}
	wg.Wait()
}
