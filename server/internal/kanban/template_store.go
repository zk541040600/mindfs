package kanban

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	configpkg "mindfs/server/internal/config"
)

const (
	stageTemplateFile = "stage_template.json"
	taskTemplateFile  = "task_template.json"
)

type TemplateStore struct {
	dir string
	mu  sync.Mutex
	now func() time.Time
}

func NewTemplateStore() (*TemplateStore, error) {
	dir, err := configpkg.MindFSConfigDir()
	if err != nil {
		return nil, err
	}
	return &TemplateStore{dir: dir, now: time.Now}, nil
}

func NewTemplateStoreAt(dir string) *TemplateStore {
	return &TemplateStore{dir: dir, now: time.Now}
}

func (s *TemplateStore) ListStageTemplates() ([]StageTemplate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.loadStageTemplatesLocked()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(items, func(i, j int) bool {
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
	return items, nil
}

func (s *TemplateStore) SaveStageTemplate(in StageTemplate) (StageTemplate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.loadStageTemplatesLocked()
	if err != nil {
		return StageTemplate{}, err
	}
	now := s.now().UTC()
	in = normalizeStageTemplate(in)
	if in.Name == "" {
		return StageTemplate{}, errors.New("stage template name required")
	}
	if in.Role != RoleUser && in.Role != RoleAgent {
		return StageTemplate{}, errors.New("invalid stage role")
	}
	if in.Role == RoleAgent {
		if strings.TrimSpace(in.Agent) == "" {
			return StageTemplate{}, errors.New("agent stage requires agent")
		}
		if strings.TrimSpace(in.Model) == "" {
			return StageTemplate{}, errors.New("agent stage requires model")
		}
	}
	if in.ID == "" {
		in.ID = newID("stage")
		in.CreatedAt = now
	}
	in.UpdatedAt = now
	if in.CreatedAt.IsZero() {
		in.CreatedAt = now
	}
	replaced := false
	for i := range items {
		if items[i].ID == in.ID {
			items[i] = in
			replaced = true
			break
		}
	}
	if !replaced {
		items = append(items, in)
	}
	return in, s.saveJSONLocked(stageTemplateFile, items)
}

func (s *TemplateStore) DeleteStageTemplate(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("stage template id required")
	}
	items, err := s.loadStageTemplatesLocked()
	if err != nil {
		return err
	}
	next := items[:0]
	removed := false
	for _, item := range items {
		if item.ID == id {
			removed = true
			continue
		}
		next = append(next, item)
	}
	if !removed {
		return errors.New("stage template not found")
	}
	return s.saveJSONLocked(stageTemplateFile, next)
}

func (s *TemplateStore) ListTaskTemplates() ([]TaskTemplate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.loadTaskTemplatesLocked()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(items, func(i, j int) bool {
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
	return items, nil
}

func (s *TemplateStore) GetTaskTemplate(id string) (TaskTemplate, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return TaskTemplate{}, errors.New("task template id required")
	}
	items, err := s.ListTaskTemplates()
	if err != nil {
		return TaskTemplate{}, err
	}
	for _, item := range items {
		if item.ID == id {
			return item, nil
		}
	}
	return TaskTemplate{}, errors.New("task template not found")
}

func (s *TemplateStore) SaveTaskTemplate(in TaskTemplate) (TaskTemplate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.loadTaskTemplatesLocked()
	if err != nil {
		return TaskTemplate{}, err
	}
	now := s.now().UTC()
	in = normalizeTaskTemplate(in)
	if in.Name == "" {
		return TaskTemplate{}, errors.New("task template name required")
	}
	if len(in.Stages) == 0 {
		return TaskTemplate{}, errors.New("task template requires stages")
	}
	sort.SliceStable(in.Stages, func(i, j int) bool { return in.Stages[i].Position < in.Stages[j].Position })
	if in.Stages[0].Snapshot.Role != RoleUser {
		return TaskTemplate{}, errors.New("first stage must be user")
	}
	if in.ID == "" {
		in.ID = newID("tmpl")
		in.CreatedAt = now
	}
	in.UpdatedAt = now
	if in.CreatedAt.IsZero() {
		in.CreatedAt = now
	}
	for i := range in.Stages {
		if in.Stages[i].ID == "" {
			in.Stages[i].ID = newID("tmpl_stage")
		}
		in.Stages[i].Position = i
		in.Stages[i].Snapshot = normalizeStageTemplate(in.Stages[i].Snapshot)
		if in.Stages[i].Snapshot.Role == RoleAgent {
			if strings.TrimSpace(in.Stages[i].Snapshot.Agent) == "" {
				return TaskTemplate{}, errors.New("agent stage requires agent")
			}
			if strings.TrimSpace(in.Stages[i].Snapshot.Model) == "" {
				return TaskTemplate{}, errors.New("agent stage requires model")
			}
		}
	}
	if in.MaxConcurrency <= 0 {
		in.MaxConcurrency = 1
	}
	replaced := false
	for i := range items {
		if items[i].ID == in.ID {
			items[i] = in
			replaced = true
			break
		}
	}
	if !replaced {
		items = append(items, in)
	}
	return in, s.saveJSONLocked(taskTemplateFile, items)
}

func (s *TemplateStore) DeleteTaskTemplate(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("task template id required")
	}
	items, err := s.loadTaskTemplatesLocked()
	if err != nil {
		return err
	}
	next := items[:0]
	removed := false
	for _, item := range items {
		if item.ID == id {
			removed = true
			continue
		}
		next = append(next, item)
	}
	if !removed {
		return errors.New("task template not found")
	}
	return s.saveJSONLocked(taskTemplateFile, next)
}

func (s *TemplateStore) loadStageTemplatesLocked() ([]StageTemplate, error) {
	var items []StageTemplate
	if err := s.loadJSONLocked(stageTemplateFile, &items); err != nil {
		return nil, err
	}
	if items == nil {
		items = []StageTemplate{}
	}
	return items, nil
}

func (s *TemplateStore) loadTaskTemplatesLocked() ([]TaskTemplate, error) {
	var items []TaskTemplate
	if err := s.loadJSONLocked(taskTemplateFile, &items); err != nil {
		return nil, err
	}
	if items == nil {
		items = []TaskTemplate{}
	}
	return items, nil
}

func (s *TemplateStore) loadJSONLocked(name string, out any) error {
	path := filepath.Join(s.dir, name)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

func (s *TemplateStore) saveJSONLocked(name string, value any) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(s.dir, name), data, 0o644)
}

func normalizeStageTemplate(in StageTemplate) StageTemplate {
	in.ID = strings.TrimSpace(in.ID)
	in.Name = strings.TrimSpace(in.Name)
	in.Role = strings.TrimSpace(in.Role)
	if in.Role == "" {
		in.Role = RoleUser
	}
	in.Agent = strings.TrimSpace(in.Agent)
	in.Model = strings.TrimSpace(in.Model)
	in.Mode = strings.TrimSpace(in.Mode)
	in.Effort = strings.TrimSpace(in.Effort)
	in.FastService = strings.TrimSpace(in.FastService)
	in.SessionReusePolicy = strings.TrimSpace(in.SessionReusePolicy)
	if in.SessionReusePolicy == "" {
		in.SessionReusePolicy = SessionReuseTaskMain
	}
	in.PromptTemplate = strings.TrimSpace(in.PromptTemplate)
	return in
}

func normalizeTaskTemplate(in TaskTemplate) TaskTemplate {
	in.ID = strings.TrimSpace(in.ID)
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	return in
}

func newID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return prefix + "_" + strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "")
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}
