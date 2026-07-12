package types

import (
	"context"
	"sync"
	"testing"
)

func TestTurnCancelerConcurrentLifecycle(t *testing.T) {
	var turns TurnCanceler
	start := make(chan struct{})
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for iteration := 0; iteration < 100; iteration++ {
				_, turnID := turns.Begin(context.Background())
				turns.Cancel()
				turns.End(turnID)
			}
		}()
	}
	close(start)
	workers.Wait()
}

func TestTurnCancelerOldEndDoesNotClearCurrentTurn(t *testing.T) {
	var turns TurnCanceler
	_, firstID := turns.Begin(context.Background())
	current, currentID := turns.Begin(context.Background())

	turns.End(firstID)
	turns.Cancel()
	select {
	case <-current.Done():
	default:
		t.Fatal("ending an older turn cleared the current cancel function")
	}
	turns.End(currentID)
}
