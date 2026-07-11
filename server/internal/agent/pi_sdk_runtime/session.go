package pisdkruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pisdkbridge "mindfs/server/internal/agent/pi_sdk_bridge"
	agenttypes "mindfs/server/internal/agent/types"
)

const (
	defaultNodeCommand                = "node"
	defaultCommandTimeout             = 30 * time.Second
	startupTimeout                    = 45 * time.Second
	messageEndFallbackDelay           = 1500 * time.Millisecond
	maxBufferedEventsBeforeSubscriber = 256
)

type OpenOptions struct {
	AgentName       string
	SessionKey      string
	Model           string
	Mode            string
	RootPath        string
	AgentDir        string
	Command         string
	Env             map[string]string
	ResumeSessionID string
	ProbePath       string
	Probe           bool
	TestScenario    string
}

type Runtime struct {
	mu       sync.Mutex
	sessions map[*session]struct{}
}

func NewRuntime() *Runtime {
	return &Runtime{sessions: make(map[*session]struct{})}
}

func (r *Runtime) OpenSession(ctx context.Context, opts OpenOptions) (agenttypes.Session, error) {
	if strings.TrimSpace(opts.SessionKey) == "" {
		return nil, errors.New("session key required")
	}
	probePath, err := pisdkbridge.ResolveProbePath(opts.ProbePath)
	if err != nil {
		return nil, err
	}

	args := []string{probePath, "jsonl"}
	if rootPath := strings.TrimSpace(opts.RootPath); rootPath != "" {
		args = append(args, "--cwd", rootPath)
	}
	cmd := exec.Command(resolveNodeCommand(opts.Command), args...)
	if rootPath := strings.TrimSpace(opts.RootPath); rootPath != "" {
		cmd.Dir = rootPath
	}
	cmd.Env = mergeEnv(opts.Env)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	sessionID := strings.TrimSpace(opts.ResumeSessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(opts.SessionKey)
	}
	s := &session{
		runtime:            r,
		cmd:                cmd,
		stdin:              stdin,
		sessionKey:         strings.TrimSpace(opts.SessionKey),
		agentName:          strings.TrimSpace(opts.AgentName),
		sessionID:          sessionID,
		model:              strings.TrimSpace(opts.Model),
		mode:               strings.TrimSpace(opts.Mode),
		pending:            make(map[string]chan bridgeResponse),
		pendingExtensionUI: make(map[string]string),
		pendingQuestions:   make(map[string]struct{}),
		turnDone:           make(chan error, 1),
		closed:             make(chan struct{}),
	}
	r.register(s)
	go s.readLoop(stdout)
	go s.stderrLoop(stderr)
	go s.waitLoop()

	startCtx, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()
	startPayload := startPayloadForOptions(opts)
	startResp, err := s.request(startCtx, "start", startPayload)
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	s.applyStartResponse(startResp)
	return s, nil
}

func startPayloadForOptions(opts OpenOptions) map[string]any {
	if scenario := strings.TrimSpace(opts.TestScenario); scenario != "" {
		return map[string]any{"type": "start_test_runtime", "scenario": scenario}
	}
	payload := map[string]any{"type": "start_sdk_runtime"}
	if agentDir := strings.TrimSpace(opts.AgentDir); agentDir != "" {
		payload["agentDir"] = agentDir
	}
	if model := strings.TrimSpace(opts.Model); model != "" {
		payload["model"] = model
	}
	if mode := strings.TrimSpace(opts.Mode); mode != "" {
		payload["mode"] = mode
	}
	if sessionID := strings.TrimSpace(opts.ResumeSessionID); sessionID != "" {
		payload["sessionId"] = sessionID
	}
	return payload
}

// applyStartResponse mirrors SDK startup metadata into the runtime cache before the first prompt is persisted.
func (s *session) applyStartResponse(resp bridgeResponse) {
	if len(resp.Data) == 0 {
		return
	}
	var data struct {
		SessionID     string          `json:"sessionId"`
		ThinkingLevel string          `json:"thinkingLevel"`
		Model         json.RawMessage `json:"model"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return
	}
	s.mu.Lock()
	if sessionID := strings.TrimSpace(data.SessionID); sessionID != "" {
		s.sessionID = sessionID
	}
	if thinkingLevel := strings.TrimSpace(data.ThinkingLevel); thinkingLevel != "" {
		s.mode = thinkingLevel
	}
	if model := parseStartResponseModel(data.Model); model != "" {
		s.model = model
	}
	s.mu.Unlock()
}

// parseStartResponseModel accepts both current object-shaped SDK models and older string-shaped model ids.
func parseStartResponseModel(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var modelName string
	if err := json.Unmarshal(raw, &modelName); err == nil {
		return normalizeModelID(modelName)
	}
	var model struct {
		ID       string `json:"id"`
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal(raw, &model); err != nil {
		return ""
	}
	provider := strings.TrimSpace(model.Provider)
	id := strings.TrimSpace(model.ID)
	switch {
	case provider != "" && id != "":
		return normalizeModelID(provider + "/" + id)
	case id != "":
		return normalizeModelID(id)
	default:
		return ""
	}
}

func (r *Runtime) Close(agentName string) error {
	if r == nil {
		return nil
	}
	agentName = strings.TrimSpace(agentName)
	r.mu.Lock()
	sessions := make([]*session, 0, len(r.sessions))
	for s := range r.sessions {
		if agentName == "" || strings.TrimSpace(s.agentName) == agentName {
			sessions = append(sessions, s)
		}
	}
	r.mu.Unlock()
	for _, s := range sessions {
		_ = s.Close()
	}
	return nil
}

func (r *Runtime) CloseAll() {
	_ = r.Close("")
}

func (r *Runtime) register(s *session) {
	if r == nil || s == nil {
		return
	}
	r.mu.Lock()
	r.sessions[s] = struct{}{}
	r.mu.Unlock()
}

func (r *Runtime) unregister(s *session) {
	if r == nil || s == nil {
		return
	}
	r.mu.Lock()
	delete(r.sessions, s)
	r.mu.Unlock()
}

type session struct {
	runtime *Runtime
	cmd     *exec.Cmd
	stdin   io.WriteCloser

	sessionKey string
	agentName  string

	seq uint64

	writeMu            sync.Mutex
	mu                 sync.RWMutex
	pending            map[string]chan bridgeResponse
	pendingExtensionUI map[string]string
	pendingQuestions   map[string]struct{}

	onUpdate                func(agenttypes.Event)
	sessionID               string
	model                   string
	mode                    string
	contextStats            agenttypes.ContextWindow
	eventBacklog            []agenttypes.Event
	seenText                bool
	seenThinking            bool
	messageTextSnapshot     string
	messageThinkingSnapshot string
	lastTurnErr             string
	turnComplete            bool
	turn                    agenttypes.TurnCanceler
	turnMu                  sync.Mutex
	turnDone                chan error

	closeOnce sync.Once
	closed    chan struct{}
}

type bridgeResponse struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Command string          `json:"command,omitempty"`
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

func resolveNodeCommand(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return defaultNodeCommand
	}
	base := strings.ToLower(filepath.Base(command))
	if base == "pi" || base == "pi.exe" {
		return defaultNodeCommand
	}
	return command
}

func mergeEnv(extra map[string]string) []string {
	env := os.Environ()
	for key, value := range extra {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		env = append(env, key+"="+value)
	}
	return env
}

func (s *session) nextID(prefix string) string {
	seq := atomic.AddUint64(&s.seq, 1)
	return fmt.Sprintf("%s-%s-%d", prefix, s.sessionKey, seq)
}

func (s *session) request(ctx context.Context, prefix string, payload map[string]any) (bridgeResponse, error) {
	return s.requestWithID(ctx, s.nextID(prefix), payload)
}

func (s *session) requestWithID(ctx context.Context, id string, payload map[string]any) (bridgeResponse, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return bridgeResponse{}, errors.New("pi sdk runtime request id required")
	}
	if payload == nil {
		payload = make(map[string]any)
	}
	payload["id"] = id
	ch := make(chan bridgeResponse, 1)

	s.mu.Lock()
	select {
	case <-s.closed:
		s.mu.Unlock()
		return bridgeResponse{}, errors.New("pi sdk runtime session closed")
	default:
	}
	s.pending[id] = ch
	s.mu.Unlock()

	if err := s.writeJSON(payload); err != nil {
		s.deletePending(id)
		return bridgeResponse{}, err
	}

	select {
	case resp := <-ch:
		if !resp.Success {
			return resp, resp.error()
		}
		return resp, nil
	case <-ctx.Done():
		s.deletePending(id)
		return bridgeResponse{}, ctx.Err()
	case <-s.closed:
		s.deletePending(id)
		return bridgeResponse{}, errors.New("pi sdk runtime session closed")
	}
}

func (r bridgeResponse) error() error {
	if r.Success {
		return nil
	}
	fallback := "pi sdk runtime command failed"
	if command := strings.TrimSpace(r.Command); command != "" {
		fallback = "pi sdk runtime " + command + " failed"
	}
	if len(r.Error) == 0 {
		return errors.New(fallback)
	}
	var structured struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(r.Error, &structured); err == nil {
		code := strings.TrimSpace(structured.Code)
		message := strings.TrimSpace(structured.Message)
		switch {
		case code != "" && message != "":
			return fmt.Errorf("%s: %s", code, message)
		case message != "":
			return errors.New(message)
		case code != "":
			return errors.New(code)
		}
	}
	var text string
	if err := json.Unmarshal(r.Error, &text); err == nil && strings.TrimSpace(text) != "" {
		return errors.New(strings.TrimSpace(text))
	}
	return errors.New(fallback)
}

func responseFromError(err error) bridgeResponse {
	if err == nil {
		err = errors.New("pi sdk runtime process exited")
	}
	raw, _ := json.Marshal(map[string]string{"code": "E_CLOSED", "message": err.Error()})
	return bridgeResponse{Type: "response", Success: false, Error: raw}
}

func (s *session) deletePending(id string) {
	s.mu.Lock()
	delete(s.pending, id)
	s.mu.Unlock()
}

func (s *session) writeJSON(payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.stdin == nil {
		return errors.New("pi sdk runtime stdin closed")
	}
	_, err = s.stdin.Write(append(raw, '\n'))
	return err
}

func (s *session) readLoop(stdout io.Reader) {
	reader := bufio.NewReader(stdout)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			s.handleLine(strings.TrimRight(line, "\r\n"))
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !s.isClosed() {
				log.Printf("[agent/pi-sdk] stdout.error session=%s err=%v", s.sessionKey, err)
			}
			s.failPending(err)
			return
		}
	}
}

func (s *session) stderrLoop(stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			log.Printf("[agent/pi-sdk][stderr] agent=%s %s", s.agentName, line)
		}
	}
}

func (s *session) waitLoop() {
	err := s.cmd.Wait()
	if err != nil && !s.isClosed() {
		log.Printf("[agent/pi-sdk] process.exit session=%s err=%v", s.sessionKey, err)
	}
	s.closeWithError(err)
	if s.runtime != nil {
		s.runtime.unregister(s)
	}
}

func (s *session) closeWithError(err error) {
	s.closeOnce.Do(func() {
		close(s.closed)
		s.failPending(err)
	})
}

func (s *session) failPending(err error) {
	resp := responseFromError(err)
	s.mu.Lock()
	pending := s.pending
	s.pending = make(map[string]chan bridgeResponse)
	s.pendingExtensionUI = make(map[string]string)
	s.mu.Unlock()
	for _, ch := range pending {
		select {
		case ch <- resp:
		default:
		}
	}
}

func (s *session) handleLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	var envelope struct {
		ID     string `json:"id,omitempty"`
		Type   string `json:"type"`
		Method string `json:"method,omitempty"`
	}
	if err := json.Unmarshal([]byte(line), &envelope); err != nil {
		log.Printf("[agent/pi-sdk] stdout.non_json session=%s line=%q", s.sessionKey, preview(line))
		return
	}
	switch envelope.Type {
	case "response":
		var resp bridgeResponse
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			log.Printf("[agent/pi-sdk] response.decode_error session=%s err=%v", s.sessionKey, err)
			return
		}
		s.mu.Lock()
		ch := s.pending[resp.ID]
		delete(s.pending, resp.ID)
		s.mu.Unlock()
		if ch != nil {
			ch <- resp
		}
	case "extension_ui_request":
		s.handleExtensionUIRequest([]byte(line), envelope.ID, envelope.Method)
	default:
		s.handleEvent([]byte(line), envelope.Type)
	}
}

func (s *session) handleExtensionUIRequest(raw []byte, id, method string) {
	id = strings.TrimSpace(id)
	method = strings.TrimSpace(method)
	if id == "" || method == "" {
		return
	}
	if isExtensionUIDialogMethod(method) {
		s.mu.Lock()
		s.pendingExtensionUI[id] = method
		s.mu.Unlock()
	}
	s.emit(agenttypes.Event{Type: agenttypes.EventTypeExtensionUI, SessionID: s.SessionID(), Data: agenttypes.ExtensionUIRequest{
		ID:      id,
		Method:  method,
		Payload: extensionUIPayload(raw),
	}})
}

func extensionUIPayload(raw []byte) map[string]any {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	delete(payload, "type")
	delete(payload, "id")
	delete(payload, "method")
	if len(payload) == 0 {
		return nil
	}
	return payload
}

func isExtensionUIDialogMethod(method string) bool {
	switch strings.TrimSpace(method) {
	case "select", "confirm", "input", "editor":
		return true
	default:
		return false
	}
}

func (s *session) handleEvent(raw []byte, eventType string) {
	switch eventType {
	case "agent_start":
		s.resetTurnState()
	case "message_start":
		s.resetMessageDeltaState()
	case "message_update":
		s.handleMessageUpdate(raw)
	case "message_end":
		s.handleMessageEnd(raw)
	case "context_window":
		s.handleContextWindow(raw)
	case "agent_end":
		s.handleAgentEnd(raw)
	case "turn_end":
		s.handleTurnEnd(raw)
	case "thinking_level_changed", "thinking_level_select":
		s.handleThinkingLevelChanged(raw)
	case "model_select":
		s.handleModelSelect(raw)
	case "tool_execution_start":
		s.handleToolExecutionStart(raw)
	case "tool_execution_update":
		s.handleToolExecutionUpdate(raw)
	case "tool_execution_end":
		s.handleToolExecutionEnd(raw)
	case "recovery", "auto_retry_start", "auto_retry_end", "compaction_start", "compaction_end":
		s.handleRecovery(raw)
	case "queue_update", "turn_start", "session_info_changed":
	default:
		log.Printf("[agent/pi-sdk] stdout.ignored_event session=%s type=%q", s.sessionKey, eventType)
	}
}

func (s *session) resetTurnState() {
	s.mu.Lock()
	s.seenText = false
	s.seenThinking = false
	s.messageTextSnapshot = ""
	s.messageThinkingSnapshot = ""
	s.lastTurnErr = ""
	s.turnComplete = false
	s.mu.Unlock()
}

func (s *session) resetMessageDeltaState() {
	s.mu.Lock()
	s.seenText = false
	s.seenThinking = false
	s.messageTextSnapshot = ""
	s.messageThinkingSnapshot = ""
	s.mu.Unlock()
}

func messageSnapshotDelta(prev, next string) string {
	if next == "" {
		return ""
	}
	if prev == "" {
		return next
	}
	if strings.HasPrefix(next, prev) {
		return next[len(prev):]
	}
	return next
}

func assistantContentText(items []struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Thinking string `json:"thinking"`
}) string {
	var b strings.Builder
	for _, item := range items {
		itemType := strings.ToLower(strings.TrimSpace(item.Type))
		if item.Text == "" || itemType == "thinking" || itemType == "reasoning" || itemType == "thought" {
			continue
		}
		b.WriteString(item.Text)
	}
	return b.String()
}

func assistantContentThinking(items []struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Thinking string `json:"thinking"`
}) string {
	var b strings.Builder
	for _, item := range items {
		thinking := item.Thinking
		if thinking == "" {
			itemType := strings.ToLower(strings.TrimSpace(item.Type))
			if itemType == "thinking" || itemType == "reasoning" || itemType == "thought" {
				thinking = item.Text
			}
		}
		if thinking == "" {
			continue
		}
		b.WriteString(thinking)
	}
	return b.String()
}

func (s *session) emitMessageTextDelta(delta string) bool {
	if delta == "" {
		return false
	}
	s.mu.Lock()
	s.seenText = true
	s.messageTextSnapshot += delta
	s.mu.Unlock()
	s.emit(agenttypes.Event{Type: agenttypes.EventTypeMessageChunk, SessionID: s.SessionID(), Data: agenttypes.MessageChunk{Content: delta}})
	return true
}

func (s *session) emitMessageTextSnapshot(text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	s.mu.Lock()
	delta := messageSnapshotDelta(s.messageTextSnapshot, text)
	if delta != "" {
		s.seenText = true
		s.messageTextSnapshot = text
	}
	s.mu.Unlock()
	if delta == "" {
		return false
	}
	s.emit(agenttypes.Event{Type: agenttypes.EventTypeMessageChunk, SessionID: s.SessionID(), Data: agenttypes.MessageChunk{Content: delta}})
	return true
}

func (s *session) emitMessageThinkingDelta(delta string) bool {
	if delta == "" {
		return false
	}
	s.mu.Lock()
	s.seenThinking = true
	s.messageThinkingSnapshot += delta
	s.mu.Unlock()
	s.emit(agenttypes.Event{Type: agenttypes.EventTypeThoughtChunk, SessionID: s.SessionID(), Data: agenttypes.ThoughtChunk{Content: delta}})
	return true
}

func (s *session) emitMessageThinkingSnapshot(thinking string) bool {
	if strings.TrimSpace(thinking) == "" {
		return false
	}
	s.mu.Lock()
	delta := messageSnapshotDelta(s.messageThinkingSnapshot, thinking)
	if delta != "" {
		s.seenThinking = true
		s.messageThinkingSnapshot = thinking
	}
	s.mu.Unlock()
	if delta == "" {
		return false
	}
	s.emit(agenttypes.Event{Type: agenttypes.EventTypeThoughtChunk, SessionID: s.SessionID(), Data: agenttypes.ThoughtChunk{Content: delta}})
	return true
}

func (s *session) handleMessageUpdate(raw []byte) {
	var ev struct {
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				Thinking string `json:"thinking"`
			} `json:"content"`
		} `json:"message"`
		AssistantMessageEvent struct {
			Type     string `json:"type"`
			Delta    string `json:"delta"`
			Content  string `json:"content"`
			Thinking string `json:"thinking"`
		} `json:"assistantMessageEvent"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return
	}
	if ev.Message.Role != "" && ev.Message.Role != "assistant" {
		return
	}
	switch ev.AssistantMessageEvent.Type {
	case "text_delta":
		if s.emitMessageTextDelta(ev.AssistantMessageEvent.Delta) {
			return
		}
	case "thinking_delta":
		if s.emitMessageThinkingDelta(ev.AssistantMessageEvent.Delta) {
			return
		}
	case "text_end":
		if s.emitMessageTextSnapshot(ev.AssistantMessageEvent.Content) {
			return
		}
	case "thinking_end":
		thinking := ev.AssistantMessageEvent.Thinking
		if thinking == "" {
			thinking = ev.AssistantMessageEvent.Content
		}
		if s.emitMessageThinkingSnapshot(thinking) {
			return
		}
	}
	if text := assistantContentText(ev.Message.Content); text != "" {
		s.emitMessageTextSnapshot(text)
	}
	if thinking := assistantContentThinking(ev.Message.Content); thinking != "" {
		s.emitMessageThinkingSnapshot(thinking)
	}
}

func (s *session) handleMessageEnd(raw []byte) {
	var ev struct {
		Message struct {
			Role         string `json:"role"`
			StopReason   string `json:"stopReason"`
			ErrorMessage string `json:"errorMessage"`
			Content      []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				Thinking string `json:"thinking"`
			} `json:"content"`
			Usage struct {
				Input       int `json:"input"`
				Output      int `json:"output"`
				CacheRead   int `json:"cacheRead"`
				CacheWrite  int `json:"cacheWrite"`
				TotalTokens int `json:"totalTokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return
	}
	if ev.Message.Role != "assistant" {
		return
	}
	if msg := strings.TrimSpace(ev.Message.ErrorMessage); msg != "" || ev.Message.StopReason == "error" || ev.Message.StopReason == "aborted" {
		if msg == "" {
			msg = "pi sdk prompt " + strings.TrimSpace(ev.Message.StopReason)
		}
		s.setLastTurnErr(msg)
		s.emit(agenttypes.Event{Type: agenttypes.EventTypeRecovery, SessionID: s.SessionID(), Data: agenttypes.RecoveryStatus{Message: msg}})
		return
	}
	if text := assistantContentText(ev.Message.Content); text != "" {
		s.emitMessageTextSnapshot(text)
	}
	if thinking := assistantContentThinking(ev.Message.Content); thinking != "" {
		s.emitMessageThinkingSnapshot(thinking)
	}
	total := ev.Message.Usage.TotalTokens
	if total == 0 {
		total = ev.Message.Usage.Input + ev.Message.Usage.Output + ev.Message.Usage.CacheRead + ev.Message.Usage.CacheWrite
	}
	if total > 0 {
		s.mu.Lock()
		s.contextStats.TotalTokens = total
		s.mu.Unlock()
	}
}

func (s *session) handleContextWindow(raw []byte) {
	var ev struct {
		TotalTokens        int `json:"totalTokens"`
		ModelContextWindow int `json:"modelContextWindow"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return
	}
	s.mu.Lock()
	if ev.TotalTokens > 0 {
		s.contextStats.TotalTokens = ev.TotalTokens
	}
	if ev.ModelContextWindow > 0 {
		s.contextStats.ModelContextWindow = ev.ModelContextWindow
	}
	s.mu.Unlock()
}

func (s *session) handleRecovery(raw []byte) {
	var ev struct {
		Message      string `json:"message"`
		ErrorMessage string `json:"errorMessage"`
	}
	_ = json.Unmarshal(raw, &ev)
	message := strings.TrimSpace(ev.Message)
	if message == "" {
		message = strings.TrimSpace(ev.ErrorMessage)
	}
	if message == "" {
		return
	}
	s.setLastTurnErr(message)
	s.emit(agenttypes.Event{Type: agenttypes.EventTypeRecovery, SessionID: s.SessionID(), Data: agenttypes.RecoveryStatus{Message: message}})
}

func (s *session) handleAgentEnd(raw []byte) {
	var ev struct {
		WillRetry     bool   `json:"willRetry"`
		PromptDone    *bool  `json:"promptDone"`
		Terminal      *bool  `json:"terminal"`
		StopReason    string `json:"stopReason"`
		ErrorMessage  string `json:"errorMessage"`
		Error         string `json:"error"`
		Cancelled     bool   `json:"cancelled"`
		Canceled      bool   `json:"canceled"`
		AbortReason   string `json:"abortReason"`
		FailureReason string `json:"failureReason"`
	}
	_ = json.Unmarshal(raw, &ev)
	if ev.WillRetry {
		return
	}
	if ev.PromptDone != nil && !*ev.PromptDone {
		return
	}
	if ev.Terminal != nil && !*ev.Terminal {
		return
	}
	if message := terminalErrorMessage(ev.StopReason, ev.ErrorMessage, ev.Error, ev.AbortReason, ev.FailureReason, ev.Cancelled, ev.Canceled); message != "" {
		s.setLastTurnErr(message)
		s.emit(agenttypes.Event{Type: agenttypes.EventTypeRecovery, SessionID: s.SessionID(), Data: agenttypes.RecoveryStatus{Message: message}})
	}
	s.completeTurn()
}

func (s *session) handleTurnEnd(raw []byte) {
	var ev struct {
		WillRetry     bool   `json:"willRetry"`
		StopReason    string `json:"stopReason"`
		ErrorMessage  string `json:"errorMessage"`
		Error         string `json:"error"`
		Cancelled     bool   `json:"cancelled"`
		Canceled      bool   `json:"canceled"`
		AbortReason   string `json:"abortReason"`
		FailureReason string `json:"failureReason"`
	}
	_ = json.Unmarshal(raw, &ev)
	if ev.WillRetry {
		return
	}
	if message := terminalErrorMessage(ev.StopReason, ev.ErrorMessage, ev.Error, ev.AbortReason, ev.FailureReason, ev.Cancelled, ev.Canceled); message != "" {
		s.setLastTurnErr(message)
		s.emit(agenttypes.Event{Type: agenttypes.EventTypeRecovery, SessionID: s.SessionID(), Data: agenttypes.RecoveryStatus{Message: message}})
	}
	// Pi SDK turn_end is one model/tool turn, not the whole agent run. Do not
	// complete MindFS SendMessage here: tool-call turns and compaction/retry can
	// legitimately continue with another LLM turn before the SDK prompt settles.
}

func (s *session) handleThinkingLevelChanged(raw []byte) {
	var ev struct {
		Level string `json:"level"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return
	}
	if level := strings.TrimSpace(ev.Level); level != "" {
		s.mu.Lock()
		s.mode = level
		s.mu.Unlock()
	}
}

func (s *session) handleModelSelect(raw []byte) {
	var ev struct {
		Model json.RawMessage `json:"model"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return
	}
	if model := parseStartResponseModel(ev.Model); model != "" {
		s.mu.Lock()
		s.model = model
		s.mu.Unlock()
	}
}

func terminalErrorMessage(stopReason, errorMessage, errText, abortReason, failureReason string, cancelled, canceled bool) string {
	message := strings.TrimSpace(errorMessage)
	if message == "" {
		message = strings.TrimSpace(errText)
	}
	if message == "" {
		message = strings.TrimSpace(abortReason)
	}
	if message == "" {
		message = strings.TrimSpace(failureReason)
	}
	stopReason = strings.TrimSpace(stopReason)
	lowerStopReason := strings.ToLower(stopReason)
	if message == "" && (lowerStopReason == "error" || lowerStopReason == "aborted" || lowerStopReason == "cancelled" || cancelled || canceled) {
		message = "pi sdk prompt " + stopReason
		if strings.TrimSpace(message) == "pi sdk prompt" {
			message = "pi sdk prompt aborted"
		}
	}
	return message
}

func (s *session) completeTurn() {
	s.completeTurnFor(nil)
}

func (s *session) completeTurnFor(ch chan error) {
	completed := false
	if ch == nil {
		completed = s.markTurnComplete()
	} else {
		completed = s.markTurnCompleteFor(ch)
	}
	if !completed {
		return
	}
	contextWindow := s.cachedContextWindow()
	s.emit(agenttypes.Event{Type: agenttypes.EventTypeMessageDone, SessionID: s.SessionID(), Data: agenttypes.MessageDone{ContextWindow: contextWindow}})
	s.mu.RLock()
	lastErr := strings.TrimSpace(s.lastTurnErr)
	s.mu.RUnlock()
	if lastErr != "" {
		if ch == nil {
			s.signalTurnDone(errors.New(lastErr))
		} else {
			s.signalTurnDoneTo(ch, errors.New(lastErr))
		}
		return
	}
	if ch == nil {
		s.signalTurnDone(nil)
	} else {
		s.signalTurnDoneTo(ch, nil)
	}
}

func (s *session) markTurnComplete() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turnComplete {
		return false
	}
	s.turnComplete = true
	return true
}

func (s *session) markTurnCompleteFor(ch chan error) bool {
	s.turnMu.Lock()
	current := s.turnDone
	s.turnMu.Unlock()
	if ch != nil && current != ch {
		return false
	}
	return s.markTurnComplete()
}

func (s *session) handleToolExecutionStart(raw []byte) {
	var ev struct {
		ToolCallID string         `json:"toolCallId"`
		ToolName   string         `json:"toolName"`
		Args       map[string]any `json:"args"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return
	}
	kind := inferToolKind(ev.ToolName)
	if kind == agenttypes.ToolKindAskUser {
		s.trackPendingQuestion(ev.ToolCallID)
	}
	if kind == agenttypes.ToolKindTodo {
		s.emitTodoUpdateFromArgs(ev.Args)
	}
	s.emit(agenttypes.Event{Type: agenttypes.EventTypeToolCall, SessionID: s.SessionID(), Data: agenttypes.ToolCall{
		CallID:    ev.ToolCallID,
		Title:     toolTitle(ev.ToolName, ev.Args),
		Status:    "running",
		Kind:      kind,
		Content:   inputContentItems(ev.ToolName, ev.Args),
		Locations: toolLocationsFromArgs(ev.Args),
		RawType:   "pi-sdk",
		Meta:      toolCallMeta(ev.ToolCallID, ev.ToolName, ev.Args),
	}})
}

func (s *session) handleToolExecutionUpdate(raw []byte) {
	var ev struct {
		ToolCallID    string          `json:"toolCallId"`
		ToolName      string          `json:"toolName"`
		Args          map[string]any  `json:"args"`
		PartialResult json.RawMessage `json:"partialResult"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return
	}
	kind := inferToolKind(ev.ToolName)
	if kind == agenttypes.ToolKindTodo {
		s.emitTodoUpdateFromArgs(ev.Args)
	}
	s.emit(agenttypes.Event{Type: agenttypes.EventTypeToolUpdate, SessionID: s.SessionID(), Data: agenttypes.ToolCall{
		CallID:    ev.ToolCallID,
		Title:     toolTitle(ev.ToolName, ev.Args),
		Status:    "running",
		Kind:      kind,
		Content:   toolResultContentItems(ev.ToolName, ev.Args, ev.PartialResult),
		Locations: mergeToolLocations(toolLocationsFromArgs(ev.Args), resultLocations(ev.PartialResult)),
		RawType:   "pi-sdk",
		Meta:      resultMeta(ev.Args, ev.PartialResult),
	}})
}

func (s *session) handleToolExecutionEnd(raw []byte) {
	var ev struct {
		ToolCallID string          `json:"toolCallId"`
		ToolName   string          `json:"toolName"`
		Args       map[string]any  `json:"args"`
		Result     json.RawMessage `json:"result"`
		IsError    bool            `json:"isError"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return
	}
	status := "complete"
	if ev.IsError {
		status = "failed"
	}
	kind := inferToolKind(ev.ToolName)
	if kind == agenttypes.ToolKindAskUser {
		s.untrackPendingQuestion(ev.ToolCallID)
	}
	if kind == agenttypes.ToolKindTodo {
		s.emitTodoUpdateFromArgs(ev.Args)
	}
	s.emit(agenttypes.Event{Type: agenttypes.EventTypeToolUpdate, SessionID: s.SessionID(), Data: agenttypes.ToolCall{
		CallID:    ev.ToolCallID,
		Title:     strings.TrimSpace(ev.ToolName),
		Status:    status,
		Kind:      kind,
		Content:   toolResultContentItems(ev.ToolName, ev.Args, ev.Result),
		Locations: mergeToolLocations(toolLocationsFromArgs(ev.Args), resultLocations(ev.Result)),
		RawType:   "pi-sdk",
		Meta:      resultMeta(ev.Args, ev.Result),
	}})
}

func (s *session) trackPendingQuestion(callID string) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return
	}
	s.mu.Lock()
	if s.pendingQuestions == nil {
		s.pendingQuestions = make(map[string]struct{})
	}
	s.pendingQuestions[callID] = struct{}{}
	s.mu.Unlock()
}

func (s *session) untrackPendingQuestion(callID string) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return
	}
	s.mu.Lock()
	delete(s.pendingQuestions, callID)
	s.mu.Unlock()
}

func (s *session) isPendingQuestion(callID string) bool {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return false
	}
	s.mu.RLock()
	_, ok := s.pendingQuestions[callID]
	s.mu.RUnlock()
	return ok
}

func (s *session) clearPendingQuestions() {
	s.mu.Lock()
	s.pendingQuestions = make(map[string]struct{})
	s.mu.Unlock()
}

func (s *session) emitTodoUpdateFromArgs(args map[string]any) {
	update, ok := todoUpdateFromArgs(args)
	if !ok {
		return
	}
	s.emit(agenttypes.Event{Type: agenttypes.EventTypeTodoUpdate, SessionID: s.SessionID(), Data: update})
}

func todoUpdateFromArgs(args map[string]any) (agenttypes.TodoUpdate, bool) {
	todos, ok := args["todos"].([]any)
	if !ok || len(todos) == 0 {
		return agenttypes.TodoUpdate{}, false
	}
	items := make([]agenttypes.TodoItem, 0, len(todos))
	for _, raw := range todos {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		content, _ := item["content"].(string)
		activeForm, _ := item["activeForm"].(string)
		status, _ := item["status"].(string)
		content = strings.TrimSpace(content)
		activeForm = strings.TrimSpace(activeForm)
		status = strings.TrimSpace(status)
		if content == "" && activeForm == "" {
			continue
		}
		items = append(items, agenttypes.TodoItem{Content: content, ActiveForm: activeForm, Status: status})
	}
	if len(items) == 0 {
		return agenttypes.TodoUpdate{}, false
	}
	return agenttypes.TodoUpdate{Items: items}, true
}

func (s *session) setLastTurnErr(message string) {
	s.mu.Lock()
	s.lastTurnErr = strings.TrimSpace(message)
	s.mu.Unlock()
}

func (s *session) signalTurnDone(err error) {
	s.turnMu.Lock()
	ch := s.turnDone
	s.turnMu.Unlock()
	s.signalTurnDoneTo(ch, err)
}

func (s *session) signalTurnDoneTo(ch chan error, err error) {
	if ch == nil {
		return
	}
	select {
	case ch <- err:
	default:
	}
}

func (s *session) resetTurnDone() chan error {
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	s.turnDone = make(chan error, 1)
	return s.turnDone
}

// emit forwards runtime events to the registered subscriber, buffering startup events until the UI attaches.
func (s *session) emit(ev agenttypes.Event) {
	s.mu.Lock()
	onUpdate := s.onUpdate
	if onUpdate == nil {
		s.appendEventBacklogLocked(ev)
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	onUpdate(ev)
}

// appendEventBacklogLocked caps pre-subscriber buffering while preserving blocking extension UI requests first.
func (s *session) appendEventBacklogLocked(ev agenttypes.Event) {
	if len(s.eventBacklog) < maxBufferedEventsBeforeSubscriber {
		s.eventBacklog = append(s.eventBacklog, ev)
		return
	}
	newIsBlocking := isBlockingExtensionUIEvent(ev)
	for idx, old := range s.eventBacklog {
		if !isBlockingExtensionUIEvent(old) {
			copy(s.eventBacklog[idx:], s.eventBacklog[idx+1:])
			s.eventBacklog[len(s.eventBacklog)-1] = ev
			return
		}
	}
	if newIsBlocking {
		copy(s.eventBacklog, s.eventBacklog[1:])
		s.eventBacklog[len(s.eventBacklog)-1] = ev
	}
}

// isBlockingExtensionUIEvent reports whether losing the event would leave a pending UI request unanswerable.
func isBlockingExtensionUIEvent(ev agenttypes.Event) bool {
	if ev.Type != agenttypes.EventTypeExtensionUI {
		return false
	}
	req, ok := ev.Data.(agenttypes.ExtensionUIRequest)
	return ok && isExtensionUIDialogMethod(req.Method)
}

func (s *session) SendMessage(ctx context.Context, content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	turnCtx, turnID := s.turn.Begin(ctx)
	defer s.turn.End(turnID)
	turnDone := s.resetTurnDone()
	s.resetTurnState()
	if _, err := s.request(turnCtx, "prompt", map[string]any{"type": "prompt", "message": content}); err != nil {
		return err
	}
	if content == "/ui-demo" {
		s.emit(agenttypes.Event{Type: agenttypes.EventTypeMessageDone, SessionID: s.SessionID(), Data: agenttypes.MessageDone{ContextWindow: s.cachedContextWindow()}})
		return nil
	}
	select {
	case err := <-turnDone:
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		return nil
	case <-turnCtx.Done():
		return turnCtx.Err()
	case <-s.closed:
		return errors.New("pi sdk runtime session closed")
	}
}

func (s *session) AnswerQuestion(ctx context.Context, answer agenttypes.AskUserAnswer) error {
	callID := strings.TrimSpace(answer.ToolUseID)
	if callID == "" {
		return errors.New("toolUseId required")
	}
	answers := make(map[string]string, len(answer.Answers))
	for key, value := range answer.Answers {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key != "" && value != "" {
			answers[key] = value
		}
	}
	if len(answers) == 0 {
		return errors.New("answers required")
	}
	if !s.isPendingQuestion(callID) {
		return errors.New("question is not pending: " + callID)
	}
	_, err := s.requestWithDefaultTimeout(ctx, "answer-question", map[string]any{
		"type":      "answer_question",
		"toolUseId": callID,
		"answers":   answers,
	})
	if err != nil {
		return err
	}
	s.untrackPendingQuestion(callID)
	return nil
}

func (s *session) AnswerExtensionUI(ctx context.Context, response agenttypes.ExtensionUIResponse) error {
	requestID := strings.TrimSpace(response.RequestID)
	if requestID == "" {
		return errors.New("extension UI requestId required")
	}
	s.mu.RLock()
	method, ok := s.pendingExtensionUI[requestID]
	s.mu.RUnlock()
	if !ok {
		return errors.New("extension UI request is not pending: " + requestID)
	}
	if requested := strings.TrimSpace(response.Method); requested != "" && requested != method {
		return fmt.Errorf("extension UI method mismatch for %s: got %s want %s", requestID, requested, method)
	}

	payload := map[string]any{"type": "extension_ui_response", "id": requestID}
	if response.Cancelled {
		payload["cancelled"] = true
	} else if method == "confirm" {
		if response.Confirmed == nil {
			return errors.New("confirmed required for confirm extension UI response")
		}
		payload["confirmed"] = *response.Confirmed
	} else {
		payload["value"] = response.Value
	}
	if _, err := s.requestWithID(ctx, requestID, payload); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.pendingExtensionUI, requestID)
	s.mu.Unlock()
	return nil
}

func (s *session) CurrentModel() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.model
}

func (s *session) SetModel(ctx context.Context, model string) error {
	provider, modelID := splitModelID(model)
	if provider == "" || modelID == "" {
		return errors.New("model must be provider/modelId")
	}
	_, err := s.requestWithDefaultTimeout(ctx, "set-model", map[string]any{"type": "set_model", "provider": provider, "modelId": modelID})
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.model = provider + "/" + modelID
	s.mu.Unlock()
	return nil
}

func (s *session) ListModels(ctx context.Context) (agenttypes.ModelList, error) {
	resp, err := s.requestWithDefaultTimeout(ctx, "models", map[string]any{"type": "get_available_models"})
	if err != nil {
		return agenttypes.ModelList{}, err
	}
	var data struct {
		Models []struct {
			ID            string         `json:"id"`
			Name          string         `json:"name"`
			Provider      string         `json:"provider"`
			Reasoning     bool           `json:"reasoning"`
			ThinkingLevel map[string]any `json:"thinkingLevelMap"`
		} `json:"models"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return agenttypes.ModelList{}, err
	}
	models := make([]agenttypes.ModelInfo, 0, len(data.Models))
	for _, item := range data.Models {
		id := normalizeModelID(item.Provider + "/" + item.ID)
		if id == "/" || strings.Trim(id, "/") == "" {
			continue
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = item.ID
		}
		if item.Provider != "" && !strings.HasPrefix(strings.ToLower(name), strings.ToLower(item.Provider)+"/") {
			name = item.Provider + "/" + name
		}
		models = append(models, agenttypes.ModelInfo{ID: id, Name: name})
	}
	current := s.CurrentModel()
	if current == "" {
		_ = s.refreshState(ctx)
		current = s.CurrentModel()
	}
	return agenttypes.ModelList{CurrentModelID: current, Models: models}, nil
}

func (s *session) SetMode(ctx context.Context, mode string) error {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return nil
	}
	_, err := s.requestWithDefaultTimeout(ctx, "set-mode", map[string]any{"type": "set_thinking_level", "level": mode})
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.mode = mode
	s.mu.Unlock()
	return nil
}

func (s *session) SetPlanMode(_ context.Context, _ bool) error {
	return nil
}

func (s *session) ListModes(ctx context.Context) (agenttypes.ModeList, error) {
	_ = s.refreshState(ctx)
	modes := []agenttypes.ModeInfo{
		{ID: "off", Name: "Thinking: off"},
		{ID: "minimal", Name: "Thinking: minimal"},
		{ID: "low", Name: "Thinking: low"},
		{ID: "medium", Name: "Thinking: medium"},
		{ID: "high", Name: "Thinking: high"},
		{ID: "xhigh", Name: "Thinking: xhigh"},
		{ID: "max", Name: "Thinking: max"},
	}
	s.mu.RLock()
	current := s.mode
	s.mu.RUnlock()
	return agenttypes.ModeList{CurrentModeID: current, Modes: modes}, nil
}

func (s *session) ListCommands(ctx context.Context) (agenttypes.CommandList, error) {
	resp, err := s.requestWithDefaultTimeout(ctx, "commands", map[string]any{"type": "get_commands"})
	if err != nil {
		return agenttypes.CommandList{}, err
	}
	var data struct {
		Commands []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Source      string `json:"source"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return agenttypes.CommandList{}, err
	}
	commands := make([]agenttypes.CommandInfo, 0, len(data.Commands))
	for _, command := range data.Commands {
		name := strings.TrimSpace(command.Name)
		if name == "" {
			continue
		}
		desc := strings.TrimSpace(command.Description)
		if src := strings.TrimSpace(command.Source); src != "" && desc != "" && !strings.HasPrefix(strings.ToLower(desc), strings.ToLower(src)+":") {
			desc = src + ": " + desc
		}
		commands = append(commands, agenttypes.CommandInfo{Name: name, Description: desc})
	}
	log.Printf("[agent/pi-sdk] commands.cached session=%s count=%d", s.sessionKey, len(commands))
	return agenttypes.CommandList{Commands: commands}, nil
}

func (s *session) cancelPendingExtensionUI() {
	s.mu.Lock()
	ids := make([]string, 0, len(s.pendingExtensionUI))
	for id := range s.pendingExtensionUI {
		ids = append(ids, id)
	}
	s.pendingExtensionUI = make(map[string]string)
	s.mu.Unlock()
	for _, id := range ids {
		_ = s.writeJSON(map[string]any{"type": "extension_ui_response", "id": id, "cancelled": true})
	}
}

func (s *session) CancelCurrentTurn() error {
	s.clearPendingQuestions()
	s.cancelPendingExtensionUI()
	abortID := s.nextID("abort")
	err := s.writeJSON(map[string]any{"type": "abort", "id": abortID})
	s.turn.Cancel()
	s.signalTurnDone(context.Canceled)
	if err != nil && !strings.Contains(err.Error(), "closed") {
		return err
	}
	return nil
}

// OnUpdate registers the session event sink and replays events captured before subscription in order.
func (s *session) OnUpdate(onUpdate func(agenttypes.Event)) {
	if onUpdate == nil {
		s.mu.Lock()
		s.onUpdate = nil
		s.eventBacklog = nil
		s.mu.Unlock()
		return
	}
	for {
		s.mu.Lock()
		backlog := append([]agenttypes.Event(nil), s.eventBacklog...)
		s.eventBacklog = nil
		if len(backlog) == 0 {
			s.onUpdate = onUpdate
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		for _, ev := range backlog {
			onUpdate(ev)
		}
	}
}

func (s *session) SessionID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessionID
}

func (s *session) ContextWindow(context.Context) (agenttypes.ContextWindow, error) {
	return s.cachedContextWindow(), nil
}

func (s *session) cachedContextWindow() agenttypes.ContextWindow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.contextStats
}

func (s *session) refreshState(ctx context.Context) error {
	resp, err := s.requestWithDefaultTimeout(ctx, "state", map[string]any{"type": "get_state"})
	if err != nil {
		return err
	}
	var data struct {
		SessionID     string          `json:"sessionId"`
		ThinkingLevel string          `json:"thinkingLevel"`
		Model         json.RawMessage `json:"model"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return err
	}
	s.mu.Lock()
	if strings.TrimSpace(data.SessionID) != "" {
		s.sessionID = strings.TrimSpace(data.SessionID)
	}
	if strings.TrimSpace(data.ThinkingLevel) != "" {
		s.mode = strings.TrimSpace(data.ThinkingLevel)
	}
	if model := parseStartResponseModel(data.Model); model != "" {
		s.model = model
	}
	s.mu.Unlock()
	return nil
}

func (s *session) requestWithDefaultTimeout(ctx context.Context, prefix string, payload map[string]any) (bridgeResponse, error) {
	timeoutCtx, cancel := withDefaultTimeout(ctx)
	defer cancel()
	return s.request(timeoutCtx, prefix, payload)
}

func withDefaultTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, defaultCommandTimeout)
}

func splitModelID(model string) (string, string) {
	model = normalizeModelID(model)
	provider, id, ok := strings.Cut(model, "/")
	if !ok {
		return "", ""
	}
	return strings.TrimSpace(provider), strings.TrimSpace(id)
}

func normalizeModelID(model string) string {
	model = strings.TrimSpace(model)
	model = strings.Trim(model, "/")
	return model
}

func inferToolKind(name string) agenttypes.ToolKind {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "read", "read_file", "grep", "find", "ls":
		return agenttypes.ToolKindRead
	case "edit", "multiedit", "multi_edit", "write", "write_file":
		return agenttypes.ToolKindEdit
	case "bash", "shell", "run_command":
		return agenttypes.ToolKindExecute
	case "web_search", "search_web":
		return agenttypes.ToolKindWebSearch
	case "ask_user_question", "askuserquestion":
		return agenttypes.ToolKindAskUser
	case "todowrite", "todo_write", "todos":
		return agenttypes.ToolKindTodo
	default:
		return agenttypes.ToolKindOther
	}
}

func toolTitle(name string, args map[string]any) string {
	name = strings.TrimSpace(name)
	if cmd, ok := args["command"].(string); ok && strings.TrimSpace(cmd) != "" {
		return name + ": " + strings.TrimSpace(cmd)
	}
	if path := pathFromArgs(args); path != "" {
		return name + ": " + filepath.Base(path)
	}
	if pattern, ok := args["pattern"].(string); ok && strings.TrimSpace(pattern) != "" {
		return name + ": " + strings.TrimSpace(pattern)
	}
	if query, ok := args["query"].(string); ok && strings.TrimSpace(query) != "" {
		return name + ": " + strings.TrimSpace(query)
	}
	return name
}

func inputContentItems(name string, args map[string]any) []agenttypes.ToolCallContentItem {
	path := pathFromArgs(args)
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "edit", "multiedit", "multi_edit":
		return editInputContentItems(path, args)
	case "write", "write_file":
		if content, ok := args["content"].(string); ok && content != "" {
			return []agenttypes.ToolCallContentItem{{Type: "text", Text: content, Path: path, ChangeKind: "add"}}
		}
	case "todowrite", "todo_write", "todos":
		if text := todoInputText(args); text != "" {
			return []agenttypes.ToolCallContentItem{{Type: "text", Text: text}}
		}
	case "ask_user_question", "askuserquestion":
		if text := askUserInputText(args); text != "" {
			return []agenttypes.ToolCallContentItem{{Type: "text", Text: text}}
		}
	}
	return nil
}

func editInputContentItems(path string, args map[string]any) []agenttypes.ToolCallContentItem {
	var items []agenttypes.ToolCallContentItem
	appendEdit := func(oldText, newText string) {
		if oldText == "" && newText == "" {
			return
		}
		old := oldText
		items = append(items, agenttypes.ToolCallContentItem{Type: "diff", Path: path, OldText: &old, NewText: newText})
	}
	if edits, ok := args["edits"].([]any); ok {
		for _, raw := range edits {
			edit, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			oldText, _ := edit["oldText"].(string)
			newText, _ := edit["newText"].(string)
			appendEdit(oldText, newText)
		}
	}
	if len(items) == 0 {
		oldText, _ := args["oldText"].(string)
		newText, _ := args["newText"].(string)
		appendEdit(oldText, newText)
	}
	return items
}

func todoInputText(args map[string]any) string {
	todos, ok := args["todos"].([]any)
	if !ok || len(todos) == 0 {
		return ""
	}
	lines := make([]string, 0, len(todos))
	for _, raw := range todos {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		content, _ := item["content"].(string)
		activeForm, _ := item["activeForm"].(string)
		status, _ := item["status"].(string)
		label := strings.TrimSpace(content)
		if strings.TrimSpace(activeForm) != "" && strings.EqualFold(strings.TrimSpace(status), "in_progress") {
			label = strings.TrimSpace(activeForm)
		}
		if label == "" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(status), "completed") {
			lines = append(lines, "- [x] "+label)
		} else {
			lines = append(lines, "- [ ] "+label)
		}
	}
	return strings.Join(lines, "\n")
}

func askUserInputText(args map[string]any) string {
	questions, ok := args["questions"].([]any)
	if !ok || len(questions) == 0 {
		return ""
	}
	sections := make([]string, 0, len(questions))
	for idx, raw := range questions {
		question, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		var lines []string
		if header, _ := question["header"].(string); strings.TrimSpace(header) != "" {
			lines = append(lines, fmt.Sprintf("**Q%d · %s**", idx+1, strings.TrimSpace(header)))
		} else {
			lines = append(lines, fmt.Sprintf("**Q%d**", idx+1))
		}
		if text, _ := question["question"].(string); strings.TrimSpace(text) != "" {
			lines = append(lines, strings.TrimSpace(text))
		}
		if multi, _ := question["multiSelect"].(bool); multi {
			lines = append(lines, "_Multiple selection allowed._")
		}
		if options, ok := question["options"].([]any); ok {
			for _, rawOption := range options {
				option, ok := rawOption.(map[string]any)
				if !ok {
					continue
				}
				label, _ := option["label"].(string)
				label = strings.TrimSpace(label)
				if label == "" {
					continue
				}
				description, _ := option["description"].(string)
				if strings.TrimSpace(description) != "" {
					lines = append(lines, fmt.Sprintf("- **%s**: %s", label, strings.TrimSpace(description)))
				} else {
					lines = append(lines, "- "+label)
				}
			}
		}
		if len(lines) > 0 {
			sections = append(sections, strings.Join(lines, "\n"))
		}
	}
	return strings.Join(sections, "\n\n")
}

func toolResultContentItems(name string, args map[string]any, raw json.RawMessage) []agenttypes.ToolCallContentItem {
	resultItems := resultContentItems(raw)
	if inferToolKind(name) != agenttypes.ToolKindEdit {
		return resultItems
	}
	inputItems := inputContentItems(name, args)
	if len(inputItems) == 0 || hasStructuredChangeContent(resultItems) {
		return resultItems
	}
	if len(resultItems) == 0 {
		return inputItems
	}
	items := make([]agenttypes.ToolCallContentItem, 0, len(inputItems)+len(resultItems))
	items = append(items, inputItems...)
	items = append(items, resultItems...)
	return items
}

func hasStructuredChangeContent(items []agenttypes.ToolCallContentItem) bool {
	for _, item := range items {
		if item.Type == "diff" || strings.TrimSpace(item.ChangeKind) != "" || isUnifiedDiffText(item.Text) {
			return true
		}
	}
	return false
}

func resultContentItems(raw json.RawMessage) []agenttypes.ToolCallContentItem {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Details struct {
			Patch string `json:"patch"`
			Diff  string `json:"diff"`
		} `json:"details"`
	}
	items := make([]agenttypes.ToolCallContentItem, 0, len(result.Content)+1)
	if err := json.Unmarshal(raw, &result); err == nil {
		diffText := result.Details.Patch
		if strings.TrimSpace(diffText) == "" && strings.TrimSpace(result.Details.Diff) != "" {
			diffText = result.Details.Diff
		}
		if strings.TrimSpace(diffText) != "" {
			items = append(items, agenttypes.ToolCallContentItem{Type: "text", Text: diffText, Path: firstPatchPath(diffText)})
		}
		for _, item := range result.Content {
			if strings.TrimSpace(item.Text) != "" {
				items = append(items, agenttypes.ToolCallContentItem{Type: "text", Text: item.Text})
			}
		}
		if len(items) > 0 {
			return items
		}
	}
	text := strings.TrimSpace(rawValueString(json.RawMessage(raw)))
	if text == "" {
		return nil
	}
	return []agenttypes.ToolCallContentItem{{Type: "text", Text: text}}
}

func isUnifiedDiffText(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "diff --git ") || strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "@@ ") {
			return true
		}
	}
	return false
}

func resultLocations(raw json.RawMessage) []agenttypes.ToolCallLocation {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var result struct {
		Details struct {
			Patch string `json:"patch"`
		} `json:"details"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil
	}
	paths := patchPaths(result.Details.Patch)
	locations := make([]agenttypes.ToolCallLocation, 0, len(paths))
	for _, path := range paths {
		locations = append(locations, agenttypes.ToolCallLocation{Path: path})
	}
	return locations
}

func toolLocationsFromArgs(args map[string]any) []agenttypes.ToolCallLocation {
	path := pathFromArgs(args)
	if path == "" {
		return nil
	}
	return []agenttypes.ToolCallLocation{{Path: path}}
}

func pathFromArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	for _, key := range []string{"path", "file_path", "filePath"} {
		path, ok := args[key].(string)
		if ok && strings.TrimSpace(path) != "" {
			return strings.TrimSpace(path)
		}
	}
	return ""
}

func mergeToolLocations(groups ...[]agenttypes.ToolCallLocation) []agenttypes.ToolCallLocation {
	seen := make(map[string]struct{})
	var merged []agenttypes.ToolCallLocation
	for _, group := range groups {
		for _, loc := range group {
			path := strings.TrimSpace(loc.Path)
			if path == "" {
				continue
			}
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			loc.Path = path
			merged = append(merged, loc)
		}
	}
	return merged
}

func firstPatchPath(patch string) string {
	paths := patchPaths(patch)
	if len(paths) == 0 {
		return ""
	}
	return paths[0]
}

func patchPaths(patch string) []string {
	var paths []string
	seen := make(map[string]struct{})
	for _, line := range strings.Split(patch, "\n") {
		path := ""
		switch {
		case strings.HasPrefix(line, "+++ "):
			path = strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
		case strings.HasPrefix(line, "diff --git "):
			fields := strings.Fields(line)
			if len(fields) >= 4 {
				path = fields[3]
			}
		}
		path = cleanPatchPath(path)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	return paths
}

func cleanPatchPath(path string) string {
	path = strings.TrimSpace(path)
	if beforeTab, _, ok := strings.Cut(path, "\t"); ok {
		path = strings.TrimSpace(beforeTab)
	}
	path = strings.Trim(path, "\"'")
	if path == "" || path == "/dev/null" {
		return ""
	}
	path = strings.TrimPrefix(path, "a/")
	path = strings.TrimPrefix(path, "b/")
	return strings.TrimSpace(path)
}

func toolCallMeta(callID, name string, args map[string]any) map[string]any {
	meta := map[string]any{
		"input":    rawValueString(args),
		"rawInput": args,
	}
	switch inferToolKind(name) {
	case agenttypes.ToolKindAskUser:
		if strings.TrimSpace(callID) != "" {
			meta["toolUseId"] = strings.TrimSpace(callID)
		}
		if questions, ok := args["questions"].([]any); ok {
			meta["questions"] = questions
		}
	case agenttypes.ToolKindTodo:
		if todos, ok := args["todos"].([]any); ok {
			meta["todos"] = todos
		}
	}
	return meta
}

func resultMeta(input map[string]any, raw json.RawMessage) map[string]any {
	meta := make(map[string]any)
	if len(input) > 0 {
		meta["input"] = rawValueString(input)
		meta["rawInput"] = input
	}
	if len(raw) > 0 && string(raw) != "null" {
		var value any
		if err := json.Unmarshal(raw, &value); err == nil {
			meta["output"] = rawValueString(value)
			meta["rawOutput"] = value
		}
	}
	if len(meta) == 0 {
		return nil
	}
	return meta
}

func rawValueString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case json.RawMessage:
		var decoded any
		if err := json.Unmarshal(v, &decoded); err == nil {
			return rawValueString(decoded)
		}
		return string(v)
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprint(v)
		}
		return string(raw)
	}
}

func (s *session) Close() error {
	s.closeOnce.Do(func() {
		close(s.closed)
		if s.stdin != nil {
			_ = s.stdin.Close()
		}
		if s.cmd != nil && s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		s.failPending(errors.New("pi sdk runtime session closed"))
	})
	return nil
}

func (s *session) isClosed() bool {
	select {
	case <-s.closed:
		return true
	default:
		return false
	}
}

func preview(s string) string {
	const max = 500
	if len(s) <= max {
		return s
	}
	return truncateUTF8ByBytes(s, max) + "..."
}

func truncateUTF8ByBytes(value string, maxBytes int) string {
	if maxBytes <= 0 || value == "" {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	end := 0
	for index := range value {
		if index > maxBytes {
			break
		}
		end = index
	}
	return strings.TrimSpace(value[:end])
}
