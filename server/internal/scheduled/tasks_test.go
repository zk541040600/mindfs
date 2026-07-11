package scheduled

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"mindfs/server/internal/agent"
	agenttypes "mindfs/server/internal/agent/types"
	"mindfs/server/internal/api/usecase"
	"mindfs/server/internal/fs"
	"mindfs/server/internal/preferences"
	"mindfs/server/internal/session"
)

func TestRunNowReturnsExecutionError(t *testing.T) {
	root := fs.NewRootInfo("root", "root", t.TempDir())
	registry := &scheduledTestRegistry{root: root}
	svc := NewService(registry, nil)

	created, err := svc.Create(context.Background(), SaveInput{
		RootID:   root.ID,
		Name:     "Daily",
		Enabled:  false,
		TaskCron: "0 9 * * *",
		Agent:    "pi",
		Prompt:   "hello",
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	updated, err := svc.RunNow(context.Background(), root.ID, created.ID)
	if err == nil || !strings.Contains(err.Error(), "broadcaster") {
		t.Fatalf("RunNow error = %v, want broadcaster error", err)
	}
	if updated.LastError == "" || !strings.Contains(updated.LastError, "broadcaster") {
		t.Fatalf("RunNow LastError = %q, want broadcaster error", updated.LastError)
	}
}

func TestConcurrentCreatePreservesAllTasks(t *testing.T) {
	root := fs.NewRootInfo("root", "root", t.TempDir())
	registry := &scheduledTestRegistry{root: root}
	svc := NewService(registry, nil)

	const taskCount = 40
	start := make(chan struct{})
	errs := make(chan error, taskCount)
	var wg sync.WaitGroup
	for i := 0; i < taskCount; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.Create(context.Background(), SaveInput{
				RootID:   root.ID,
				Name:     fmt.Sprintf("task-%02d", i),
				Enabled:  false,
				TaskCron: "0 9 * * *",
				Agent:    "pi",
				Prompt:   "hello",
			})
			if err != nil {
				errs <- err
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Create returned error: %v", err)
	}

	tasks, err := svc.List(context.Background(), root.ID)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(tasks) != taskCount {
		t.Fatalf("List returned %d tasks, want %d", len(tasks), taskCount)
	}
	seen := make(map[string]bool, taskCount)
	for _, task := range tasks {
		if seen[task.Name] {
			t.Fatalf("duplicate task name %q in %+v", task.Name, tasks)
		}
		seen[task.Name] = true
	}
	for i := 0; i < taskCount; i++ {
		name := fmt.Sprintf("task-%02d", i)
		if !seen[name] {
			t.Fatalf("missing task %q after concurrent creates; got %+v", name, tasks)
		}
	}
}

type scheduledTestRegistry struct {
	root fs.RootInfo
}

func (r *scheduledTestRegistry) GetRoot(rootID string) (fs.RootInfo, error) {
	if rootID == r.root.ID {
		return r.root, nil
	}
	return fs.RootInfo{}, errors.New("root not found")
}

func (r *scheduledTestRegistry) GetSessionManager(rootID string) (*session.Manager, error) {
	root, err := r.GetRoot(rootID)
	if err != nil {
		return nil, err
	}
	return session.NewManager(root), nil
}

func (r *scheduledTestRegistry) UpsertRoot(path string) (fs.RootInfo, error) { return r.root, nil }
func (r *scheduledTestRegistry) RemoveRoot(path string) (fs.RootInfo, error) { return r.root, nil }
func (r *scheduledTestRegistry) RenameRoot(rootID, name, rootPath string) (fs.RootInfo, error) {
	return r.root, nil
}
func (r *scheduledTestRegistry) ListRoots() []fs.RootInfo           { return []fs.RootInfo{r.root} }
func (r *scheduledTestRegistry) GetAgentPool() *agent.Pool          { return nil }
func (r *scheduledTestRegistry) GetPreferences() *preferences.Store { return nil }
func (r *scheduledTestRegistry) GetExternalSessionImporter(agentName string) (agenttypes.ExternalSessionImporter, error) {
	return nil, errors.New("not implemented")
}
func (r *scheduledTestRegistry) GetProber() *agent.Prober                         { return nil }
func (r *scheduledTestRegistry) GetCandidateRegistry() *usecase.CandidateRegistry { return nil }
func (r *scheduledTestRegistry) GetFileWatcher(rootID string, manager *session.Manager) (*fs.SharedFileWatcher, error) {
	return nil, errors.New("not implemented")
}
func (r *scheduledTestRegistry) ReleaseFileWatcher(rootID, sessionKey string) {}
