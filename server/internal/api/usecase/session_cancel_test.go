package usecase

import (
	"context"
	"errors"
	"testing"

	rootfs "mindfs/server/internal/fs"
	"mindfs/server/internal/session"
)

func resetActiveTurnStateForTest() {
	activeTurnsMu.Lock()
	defer activeTurnsMu.Unlock()
	activeTurns = make(map[string]*activeTurnState)
	pendingTurnCancel = make(map[string]pendingTurnCancelState)
}

func TestCancelSessionTurnBeforeActiveRegistrationCancelsMatchingRequest(t *testing.T) {
	resetActiveTurnStateForTest()
	root := rootfs.NewRootInfo("root", "root", t.TempDir())
	manager := session.NewManager(root)
	current, err := manager.Create(context.Background(), session.CreateInput{Key: "s1", Type: session.TypeChat, Agent: "pi"})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{Registry: &commandTestRegistry{root: root, manager: manager}}

	if err := svc.CancelSessionTurn(context.Background(), CancelSessionTurnInput{RootID: root.ID, Key: current.Key, RequestID: "req-1"}); err != nil {
		t.Fatalf("CancelSessionTurn returned error: %v", err)
	}

	cancelCalled := false
	cancelledBeforeStart := registerActiveTurn(root.ID, current.Key, "req-1", func() { cancelCalled = true })
	defer unregisterActiveTurn(root.ID, current.Key)
	if !cancelledBeforeStart {
		t.Fatal("matching request registration was not cancelled by early cancel tombstone")
	}
	if !cancelCalled {
		t.Fatal("matching request cancel func was not invoked")
	}
}

func TestCancelSessionTurnBeforeActiveRegistrationDoesNotCancelDifferentRequest(t *testing.T) {
	resetActiveTurnStateForTest()
	root := rootfs.NewRootInfo("root", "root", t.TempDir())
	manager := session.NewManager(root)
	current, err := manager.Create(context.Background(), session.CreateInput{Key: "s1", Type: session.TypeChat, Agent: "pi"})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{Registry: &commandTestRegistry{root: root, manager: manager}}

	if err := svc.CancelSessionTurn(context.Background(), CancelSessionTurnInput{RootID: root.ID, Key: current.Key, RequestID: "req-1"}); err != nil {
		t.Fatalf("CancelSessionTurn returned error: %v", err)
	}

	cancelCalled := false
	cancelledBeforeStart := registerActiveTurn(root.ID, current.Key, "req-2", func() { cancelCalled = true })
	defer unregisterActiveTurn(root.ID, current.Key)
	if cancelledBeforeStart {
		t.Fatal("different request registration was cancelled by stale request tombstone")
	}
	if cancelCalled {
		t.Fatal("different request cancel func was invoked")
	}
}

func TestCancelSessionTurnActiveRequestMismatchReturnsError(t *testing.T) {
	resetActiveTurnStateForTest()
	root := rootfs.NewRootInfo("root", "root", t.TempDir())
	manager := session.NewManager(root)
	current, err := manager.Create(context.Background(), session.CreateInput{Key: "s1", Type: session.TypeChat, Agent: "pi"})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{Registry: &commandTestRegistry{root: root, manager: manager}}

	cancelCalled := false
	registerActiveTurn(root.ID, current.Key, "req-2", func() { cancelCalled = true })
	defer unregisterActiveTurn(root.ID, current.Key)

	err = svc.CancelSessionTurn(context.Background(), CancelSessionTurnInput{RootID: root.ID, Key: current.Key, RequestID: "req-1"})
	if !errors.Is(err, ErrSessionCancelRequestMismatch) {
		t.Fatalf("expected ErrSessionCancelRequestMismatch, got %v", err)
	}
	if cancelCalled {
		t.Fatal("mismatched request should not be cancelled by usecase without WS retry")
	}
}
