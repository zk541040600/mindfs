package pi

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

	agenttypes "mindfs/server/internal/agent/types"
)

const (
	defaultCommandTimeout = 30 * time.Second
	startupStateTimeout   = 30 * time.Second
)

var (
	extensionOnlyProbeInterval  = 1500 * time.Millisecond
	extensionOnlyFallbackPeriod = 6 * time.Second
)

type OpenOptions struct {
	AgentName       string
	SessionKey      string
	Model           string
	Mode            string
	RootPath        string
	Command         string
	Args            []string
	Env             map[string]string
	ResumeSessionID string
}

type Runtime struct {
	mu       sync.Mutex
	sessions map[*session]struct{}
}

func NewRuntime() *Runtime {
	return &Runtime{sessions: make(map[*session]struct{})}
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

func (r *Runtime) OpenSession(ctx context.Context, opts OpenOptions) (agenttypes.Session, error) {
	return r.openSession(ctx, opts, 2)
}

func (r *Runtime) openSession(ctx context.Context, opts OpenOptions, attempts int) (agenttypes.Session, error) {
	if strings.TrimSpace(opts.SessionKey) == "" {
		return nil, errors.New("session key required")
	}
	command := strings.TrimSpace(opts.Command)
	if command == "" {
		command = "pi"
	}
	args := ensureRPCArgs(opts.Args)
	if strings.TrimSpace(opts.ResumeSessionID) != "" && !hasNoSessionArg(args) {
		args = append(args, "--session", strings.TrimSpace(opts.ResumeSessionID))
	}
	cmd := exec.Command(command, args...)
	if cwd := strings.TrimSpace(opts.RootPath); cwd != "" {
		cmd.Dir = cwd
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

	s := &session{
		runtime:    r,
		cmd:        cmd,
		stdin:      stdin,
		sessionKey: strings.TrimSpace(opts.SessionKey),
		agentName:  strings.TrimSpace(opts.AgentName),
		starting:   true,
		model:      normalizeModelID(strings.TrimSpace(opts.Model)),
		mode:       strings.TrimSpace(opts.Mode),
		pending:    make(map[string]chan rpcResponse),
		turnDone:   make(chan error, 1),
		closed:     make(chan struct{}),
	}
	r.register(s)
	go s.readLoop(stdout)
	go s.stderrLoop(stderr)
	go s.waitLoop()

	stateCtx, cancel := context.WithTimeout(ctx, startupStateTimeout)
	stateErr := s.refreshState(stateCtx)
	cancel()
	if stateErr != nil && isStartupExitError(stateErr) {
		_ = s.Close()
		if attempts > 1 && ctx.Err() == nil {
			time.Sleep(300 * time.Millisecond)
			return r.openSession(ctx, opts, attempts-1)
		}
		log.Printf("[agent/pi-rpc] startup_state.error session=%s err=%v", s.sessionKey, stateErr)
		return nil, stateErr
	}
	if stateErr != nil {
		log.Printf("[agent/pi-rpc] startup_state.warn session=%s err=%v", s.sessionKey, stateErr)
	}
	if model := normalizeModelID(strings.TrimSpace(opts.Model)); model != "" {
		if err := s.SetModel(ctx, model); err != nil && s.isClosed() {
			_ = s.Close()
			if attempts > 1 && ctx.Err() == nil {
				time.Sleep(300 * time.Millisecond)
				return r.openSession(ctx, opts, attempts-1)
			}
			return nil, err
		}
	}
	if mode := strings.TrimSpace(opts.Mode); mode != "" {
		if err := s.SetMode(ctx, mode); err != nil && s.isClosed() {
			_ = s.Close()
			if attempts > 1 && ctx.Err() == nil {
				time.Sleep(300 * time.Millisecond)
				return r.openSession(ctx, opts, attempts-1)
			}
			return nil, err
		}
	}
	if s.isClosed() {
		if attempts > 1 && ctx.Err() == nil {
			time.Sleep(300 * time.Millisecond)
			return r.openSession(ctx, opts, attempts-1)
		}
		return nil, errors.New("pi rpc process exited during startup")
	}
	s.markStartupComplete()
	return s, nil
}

func (r *Runtime) CloseAll() {
	if r == nil {
		return
	}
	r.mu.Lock()
	sessions := make([]*session, 0, len(r.sessions))
	for s := range r.sessions {
		sessions = append(sessions, s)
	}
	r.mu.Unlock()
	for _, s := range sessions {
		_ = s.Close()
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

type session struct {
	runtime *Runtime
	cmd     *exec.Cmd
	stdin   io.WriteCloser

	sessionKey string
	agentName  string

	seq uint64

	writeMu sync.Mutex
	mu      sync.RWMutex
	pending map[string]chan rpcResponse

	onUpdate           func(agenttypes.Event)
	sessionID          string
	model              string
	mode               string
	contextWindow      agenttypes.ContextWindow
	seenTextDelta      bool
	seenThinkingDelta  bool
	lastAssistantErr   string
	starting           bool
	activePromptID     string
	activePromptSlash  bool
	promptNotifySeen   bool
	promptAgentStarted bool

	turn        agenttypes.TurnCanceler
	turnMu      sync.Mutex
	turnDone    chan error
	lastTurnErr error

	closeOnce sync.Once
	closed    chan struct{}
}

type rpcResponse struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Command string          `json:"command,omitempty"`
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   string          `json:"error,omitempty"`
}

func ensureRPCArgs(args []string) []string {
	out := append([]string{}, args...)
	hasMode := false
	hasNoSession := false
	for i := 0; i < len(out); i++ {
		arg := strings.TrimSpace(out[i])
		if arg == "--mode" && i+1 < len(out) && strings.TrimSpace(out[i+1]) == "rpc" {
			hasMode = true
		}
		if arg == "--mode=rpc" {
			hasMode = true
		}
		if arg == "--no-session" {
			hasNoSession = true
		}
	}
	if !hasMode {
		out = append(out, "--mode", "rpc")
	}
	if !hasNoSession {
		out = append(out, "--no-session")
	}
	return out
}

func isStartupExitError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) {
		return true
	}
	value := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(value, "closed") ||
		strings.Contains(value, "broken pipe") ||
		strings.Contains(value, "process exited") ||
		strings.Contains(value, "file already closed") ||
		strings.Contains(value, "eof")
}

func hasNoSessionArg(args []string) bool {
	for _, arg := range args {
		if strings.TrimSpace(arg) == "--no-session" {
			return true
		}
	}
	return false
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

func (s *session) request(ctx context.Context, prefix string, payload map[string]any) (rpcResponse, error) {
	if payload == nil {
		payload = make(map[string]any)
	}
	id := s.nextID(prefix)
	payload["id"] = id
	ch := make(chan rpcResponse, 1)

	s.mu.Lock()
	if prefix == "prompt" {
		s.activePromptID = id
	}
	select {
	case <-s.closed:
		s.mu.Unlock()
		return rpcResponse{}, errors.New("pi rpc session closed")
	default:
	}
	s.pending[id] = ch
	s.mu.Unlock()

	if err := s.writeJSON(payload); err != nil {
		s.deletePending(id)
		return rpcResponse{}, err
	}

	if prefix == "prompt" {
		defer s.clearActivePromptID(id)
		promptStartedAt := time.Now()
		probe := time.NewTimer(extensionOnlyProbeInterval)
		defer probe.Stop()
		for {
			select {
			case resp := <-ch:
				if !resp.Success {
					if strings.TrimSpace(resp.Error) != "" {
						return resp, errors.New(resp.Error)
					}
					return resp, errors.New("pi rpc command failed: " + strings.TrimSpace(resp.Command))
				}
				return resp, nil
			case <-probe.C:
				activeSlash, notifySeen, agentStarted := s.promptState()
				if activeSlash && !agentStarted && (notifySeen || time.Since(promptStartedAt) >= extensionOnlyFallbackPeriod) {
					s.deletePending(id)
					return rpcResponse{ID: id, Type: "response", Command: "prompt", Success: true}, nil
				}
				probe.Reset(extensionOnlyProbeInterval)
			case <-ctx.Done():
				s.deletePending(id)
				return rpcResponse{}, ctx.Err()
			case <-s.closed:
				s.deletePending(id)
				return rpcResponse{}, errors.New("pi rpc session closed")
			}
		}
	}

	select {
	case resp := <-ch:
		if !resp.Success {
			if strings.TrimSpace(resp.Error) != "" {
				return resp, errors.New(resp.Error)
			}
			return resp, errors.New("pi rpc command failed: " + strings.TrimSpace(resp.Command))
		}
		return resp, nil
	case <-ctx.Done():
		s.deletePending(id)
		return rpcResponse{}, ctx.Err()
	case <-s.closed:
		s.deletePending(id)
		return rpcResponse{}, errors.New("pi rpc session closed")
	}
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
		return errors.New("pi rpc stdin closed")
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
				log.Printf("[agent/pi-rpc] stdout.error session=%s err=%v", s.sessionKey, err)
			}
			s.failPending(err)
			s.signalTurnDone(err)
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
			log.Printf("[agent/pi-rpc][stderr] agent=%s %s", s.agentName, line)
		}
	}
}

func (s *session) waitLoop() {
	defer s.unregisterRuntime()
	err := s.cmd.Wait()
	if err != nil && !s.isClosed() && !s.isStarting() {
		log.Printf("[agent/pi-rpc] process.exit session=%s err=%v", s.sessionKey, err)
	}
	s.closeOnce.Do(func() { close(s.closed) })
	s.failPending(err)
	s.signalTurnDone(err)
}

func (s *session) unregisterRuntime() {
	if s != nil && s.runtime != nil {
		s.runtime.unregister(s)
	}
}

func (s *session) markStartupComplete() {
	s.mu.Lock()
	s.starting = false
	s.mu.Unlock()
}

func (s *session) isStarting() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.starting
}

func (s *session) isClosed() bool {
	select {
	case <-s.closed:
		return true
	default:
		return false
	}
}

func (s *session) failPending(err error) {
	if err == nil {
		err = errors.New("pi rpc process exited")
	}
	s.mu.Lock()
	pending := s.pending
	s.pending = make(map[string]chan rpcResponse)
	s.mu.Unlock()
	for _, ch := range pending {
		select {
		case ch <- rpcResponse{Type: "response", Success: false, Error: err.Error()}:
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
		log.Printf("[agent/pi-rpc] stdout.non_json session=%s line=%q", s.sessionKey, preview(line))
		return
	}
	switch envelope.Type {
	case "response":
		var resp rpcResponse
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			log.Printf("[agent/pi-rpc] response.decode_error session=%s err=%v", s.sessionKey, err)
			return
		}
		s.mu.Lock()
		ch := s.pending[resp.ID]
		delete(s.pending, resp.ID)
		s.mu.Unlock()
		if ch != nil {
			ch <- resp
		}
		if resp.ID != "" {
			s.clearActivePromptID(resp.ID)
		}
	case "extension_ui_request":
		s.handleExtensionUIRequest([]byte(line), envelope.ID, envelope.Method)
	default:
		s.handleEvent([]byte(line), envelope.Type)
	}
}

func (s *session) handleExtensionUIRequest(raw []byte, id, method string) {
	method = strings.TrimSpace(method)
	if method == "notify" {
		s.handleNotifyRequest(raw)
	}
	switch method {
	case "select", "input", "editor":
		_ = s.writeJSON(map[string]any{"type": "extension_ui_response", "id": id, "cancelled": true})
	case "confirm":
		_ = s.writeJSON(map[string]any{"type": "extension_ui_response", "id": id, "confirmed": false})
	default:
		// Fire-and-forget UI requests (notify/status/widget/title) are intentionally ignored.
	}
}

func (s *session) handleEvent(raw []byte, eventType string) {
	switch eventType {
	case "agent_start":
		s.markPromptAgentStarted()
		s.resetDeltaState()
	case "message_start":
		s.resetDeltaState()
	case "message_update":
		s.handleMessageUpdate(raw)
	case "message_end":
		s.handleMessageEnd(raw)
	case "tool_execution_start":
		s.handleToolExecutionStart(raw)
	case "tool_execution_update":
		s.handleToolExecutionUpdate(raw)
	case "tool_execution_end":
		s.handleToolExecutionEnd(raw)
	case "agent_end":
		s.handleAgentEnd(raw)
	case "auto_retry_start":
		var ev struct {
			ErrorMessage string `json:"errorMessage"`
		}
		_ = json.Unmarshal(raw, &ev)
		if strings.TrimSpace(ev.ErrorMessage) != "" {
			s.emit(agenttypes.Event{Type: agenttypes.EventTypeRecovery, SessionID: s.SessionID(), Data: agenttypes.RecoveryStatus{Message: ev.ErrorMessage}})
		}
	}
}

func (s *session) resetDeltaState() {
	s.mu.Lock()
	s.seenTextDelta = false
	s.seenThinkingDelta = false
	s.lastAssistantErr = ""
	s.mu.Unlock()
}

func (s *session) resetPromptState(content string) {
	s.mu.Lock()
	s.activePromptID = ""
	s.activePromptSlash = strings.HasPrefix(strings.TrimSpace(content), "/")
	s.promptNotifySeen = false
	s.promptAgentStarted = false
	s.mu.Unlock()
}

func (s *session) clearPromptState() {
	s.mu.Lock()
	s.activePromptID = ""
	s.activePromptSlash = false
	s.promptNotifySeen = false
	s.promptAgentStarted = false
	s.mu.Unlock()
}

func (s *session) clearActivePromptID(id string) {
	s.mu.Lock()
	if s.activePromptID == id {
		s.activePromptID = ""
	}
	s.mu.Unlock()
}

func (s *session) markPromptAgentStarted() {
	s.mu.Lock()
	s.promptAgentStarted = true
	s.mu.Unlock()
}

func (s *session) promptState() (activeSlash bool, notifySeen bool, agentStarted bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activePromptSlash, s.promptNotifySeen, s.promptAgentStarted
}

func (s *session) handleNotifyRequest(raw []byte) {
	var ev struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return
	}
	message := strings.TrimSpace(ev.Message)
	if message == "" {
		return
	}
	s.mu.Lock()
	activeSlash := s.activePromptSlash
	agentStarted := s.promptAgentStarted
	if activeSlash && !agentStarted {
		s.promptNotifySeen = true
	}
	s.mu.Unlock()
	if !activeSlash || agentStarted {
		return
	}
	s.markTextDeltaSeen()
	s.emit(agenttypes.Event{Type: agenttypes.EventTypeMessageChunk, SessionID: s.SessionID(), Data: agenttypes.MessageChunk{Content: message}})
}

func (s *session) finishExtensionOnlyPromptIfIdle(ctx context.Context, content string) {
	if !strings.HasPrefix(strings.TrimSpace(content), "/") {
		return
	}
	timer := time.NewTimer(extensionOnlyProbeInterval)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		return
	case <-s.closed:
		return
	}
	activeSlash, _, agentStarted := s.promptState()
	if !activeSlash || agentStarted {
		return
	}
	if !s.hasTextDeltaSeen() {
		fallback := "Command handled: " + strings.TrimSpace(content)
		s.markTextDeltaSeen()
		s.emit(agenttypes.Event{Type: agenttypes.EventTypeMessageChunk, SessionID: s.SessionID(), Data: agenttypes.MessageChunk{Content: fallback}})
	}
	s.emit(agenttypes.Event{Type: agenttypes.EventTypeMessageDone, SessionID: s.SessionID(), Data: agenttypes.MessageDone{ContextWindow: s.cachedContextWindow()}})
	s.signalTurnDone(nil)
}

func (s *session) markTextDeltaSeen() {
	s.mu.Lock()
	s.seenTextDelta = true
	s.mu.Unlock()
}

func (s *session) markThinkingDeltaSeen() {
	s.mu.Lock()
	s.seenThinkingDelta = true
	s.mu.Unlock()
}

func (s *session) hasTextDeltaSeen() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.seenTextDelta
}

func (s *session) hasThinkingDeltaSeen() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.seenThinkingDelta
}

func (s *session) handleMessageUpdate(raw []byte) {
	var ev struct {
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
	switch ev.AssistantMessageEvent.Type {
	case "text_delta":
		delta := ev.AssistantMessageEvent.Delta
		if delta == "" {
			return
		}
		s.markTextDeltaSeen()
		s.emit(agenttypes.Event{Type: agenttypes.EventTypeMessageChunk, SessionID: s.SessionID(), Data: agenttypes.MessageChunk{Content: delta}})
	case "thinking_delta":
		delta := ev.AssistantMessageEvent.Delta
		if delta == "" {
			return
		}
		s.markThinkingDeltaSeen()
		s.emit(agenttypes.Event{Type: agenttypes.EventTypeThoughtChunk, SessionID: s.SessionID(), Data: agenttypes.ThoughtChunk{Content: delta}})
	case "text_end":
		content := strings.TrimSpace(ev.AssistantMessageEvent.Content)
		if content != "" && !s.hasTextDeltaSeen() {
			s.emit(agenttypes.Event{Type: agenttypes.EventTypeMessageChunk, SessionID: s.SessionID(), Data: agenttypes.MessageChunk{Content: content}})
		}
	case "thinking_end":
		content := strings.TrimSpace(ev.AssistantMessageEvent.Thinking)
		if content == "" {
			content = strings.TrimSpace(ev.AssistantMessageEvent.Content)
		}
		if content != "" && !s.hasThinkingDeltaSeen() {
			s.emit(agenttypes.Event{Type: agenttypes.EventTypeThoughtChunk, SessionID: s.SessionID(), Data: agenttypes.ThoughtChunk{Content: content}})
		}
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
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return
	}
	if ev.Message.Role != "assistant" {
		return
	}
	if msg := strings.TrimSpace(ev.Message.ErrorMessage); msg != "" {
		s.mu.Lock()
		s.lastAssistantErr = msg
		s.mu.Unlock()
		s.emit(agenttypes.Event{Type: agenttypes.EventTypeRecovery, SessionID: s.SessionID(), Data: agenttypes.RecoveryStatus{Message: msg}})
		return
	}
	if s.hasTextDeltaSeen() && s.hasThinkingDeltaSeen() {
		return
	}
	for _, item := range ev.Message.Content {
		switch item.Type {
		case "text":
			text := strings.TrimSpace(item.Text)
			if text != "" && !s.hasTextDeltaSeen() {
				s.emit(agenttypes.Event{Type: agenttypes.EventTypeMessageChunk, SessionID: s.SessionID(), Data: agenttypes.MessageChunk{Content: text}})
				s.markTextDeltaSeen()
			}
		case "thinking":
			thinking := strings.TrimSpace(item.Thinking)
			if thinking != "" && !s.hasThinkingDeltaSeen() {
				s.emit(agenttypes.Event{Type: agenttypes.EventTypeThoughtChunk, SessionID: s.SessionID(), Data: agenttypes.ThoughtChunk{Content: thinking}})
				s.markThinkingDeltaSeen()
			}
		}
	}
}

func (s *session) handleAgentEnd(raw []byte) {
	var ev struct {
		WillRetry bool `json:"willRetry"`
	}
	_ = json.Unmarshal(raw, &ev)
	if ev.WillRetry {
		return
	}
	s.emit(agenttypes.Event{Type: agenttypes.EventTypeMessageDone, SessionID: s.SessionID(), Data: agenttypes.MessageDone{ContextWindow: s.cachedContextWindow()}})
	s.mu.RLock()
	lastErr := strings.TrimSpace(s.lastAssistantErr)
	s.mu.RUnlock()
	if lastErr != "" {
		s.signalTurnDone(errors.New(lastErr))
		return
	}
	s.signalTurnDone(nil)
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
	s.emit(agenttypes.Event{Type: agenttypes.EventTypeToolCall, SessionID: s.SessionID(), Data: agenttypes.ToolCall{
		CallID:  ev.ToolCallID,
		Title:   toolTitle(ev.ToolName, ev.Args),
		Status:  "running",
		Kind:    inferToolKind(ev.ToolName),
		RawType: "pi-rpc",
		Meta:    map[string]any{"input": rawValueString(ev.Args), "rawInput": ev.Args},
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
	s.emit(agenttypes.Event{Type: agenttypes.EventTypeToolUpdate, SessionID: s.SessionID(), Data: agenttypes.ToolCall{
		CallID:  ev.ToolCallID,
		Title:   toolTitle(ev.ToolName, ev.Args),
		Status:  "running",
		Kind:    inferToolKind(ev.ToolName),
		Content: resultContentItems(ev.PartialResult),
		RawType: "pi-rpc",
		Meta:    resultMeta(ev.Args, ev.PartialResult),
	}})
}

func (s *session) handleToolExecutionEnd(raw []byte) {
	var ev struct {
		ToolCallID string          `json:"toolCallId"`
		ToolName   string          `json:"toolName"`
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
	s.emit(agenttypes.Event{Type: agenttypes.EventTypeToolUpdate, SessionID: s.SessionID(), Data: agenttypes.ToolCall{
		CallID:  ev.ToolCallID,
		Title:   strings.TrimSpace(ev.ToolName),
		Status:  status,
		Kind:    inferToolKind(ev.ToolName),
		Content: resultContentItems(ev.Result),
		RawType: "pi-rpc",
		Meta:    resultMeta(nil, ev.Result),
	}})
}

func (s *session) signalTurnDone(err error) {
	s.turnMu.Lock()
	s.lastTurnErr = err
	ch := s.turnDone
	select {
	case ch <- err:
	default:
	}
	s.turnMu.Unlock()
}

func (s *session) resetTurnDone() chan error {
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	s.turnDone = make(chan error, 1)
	s.lastTurnErr = nil
	return s.turnDone
}

func (s *session) SendMessage(ctx context.Context, content string) error {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	turnCtx, turnID := s.turn.Begin(ctx)
	defer s.turn.End(turnID)
	turnDone := s.resetTurnDone()
	s.resetDeltaState()
	s.resetPromptState(content)
	defer s.clearPromptState()
	log.Printf("[agent/pi-rpc] input session=%s content=%q", s.sessionKey, preview(content))
	_, err := s.request(turnCtx, "prompt", map[string]any{"type": "prompt", "message": content})
	if err != nil {
		return err
	}
	s.finishExtensionOnlyPromptIfIdle(turnCtx, content)
	select {
	case err := <-turnDone:
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		return nil
	case <-turnCtx.Done():
		return turnCtx.Err()
	case <-s.closed:
		return errors.New("pi rpc session closed")
	}
}

func (s *session) AnswerQuestion(context.Context, agenttypes.AskUserAnswer) error {
	return errors.New("ask user question is not supported by pi-rpc sessions")
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
	_, err := s.request(withDefaultTimeout(ctx), "set-model", map[string]any{"type": "set_model", "provider": provider, "modelId": modelID})
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.model = provider + "/" + modelID
	s.mu.Unlock()
	return nil
}

func (s *session) ListModels(ctx context.Context) (agenttypes.ModelList, error) {
	resp, err := s.request(withDefaultTimeout(ctx), "models", map[string]any{"type": "get_available_models"})
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
		// Pi exposes reasoning control through ListModes/SetMode because the RPC API
		// names the operation set_thinking_level. Do not also mark models as
		// SupportEffort, otherwise MindFS shows duplicate "模式" and "思考等级"
		// selectors that both represent the same Pi thinking level.
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
	_, err := s.request(withDefaultTimeout(ctx), "set-mode", map[string]any{"type": "set_thinking_level", "level": mode})
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.mode = mode
	s.mu.Unlock()
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
	}
	s.mu.RLock()
	current := s.mode
	s.mu.RUnlock()
	return agenttypes.ModeList{CurrentModeID: current, Modes: modes}, nil
}

func (s *session) ListCommands(ctx context.Context) (agenttypes.CommandList, error) {
	resp, err := s.request(withDefaultTimeout(ctx), "commands", map[string]any{"type": "get_commands"})
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
	log.Printf("[agent/pi-rpc] commands.cached session=%s count=%d", s.sessionKey, len(commands))
	return agenttypes.CommandList{Commands: commands}, nil
}

func (s *session) CancelCurrentTurn() error {
	s.turn.Cancel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.request(ctx, "abort", map[string]any{"type": "abort"})
	if err != nil && !strings.Contains(err.Error(), "closed") {
		return err
	}
	return nil
}

func (s *session) OnUpdate(onUpdate func(agenttypes.Event)) {
	s.mu.Lock()
	s.onUpdate = onUpdate
	s.mu.Unlock()
}

func (s *session) SessionID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessionID
}

func (s *session) ContextWindow(ctx context.Context) (agenttypes.ContextWindow, error) {
	ctx = withDefaultTimeout(ctx)
	resp, err := s.request(ctx, "stats", map[string]any{"type": "get_session_stats"})
	if err != nil {
		return s.cachedContextWindow(), nil
	}
	var data struct {
		ContextUsage struct {
			Tokens        *int `json:"tokens"`
			ContextWindow int  `json:"contextWindow"`
		} `json:"contextUsage"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return s.cachedContextWindow(), nil
	}
	cw := agenttypes.ContextWindow{ModelContextWindow: data.ContextUsage.ContextWindow}
	if data.ContextUsage.Tokens != nil {
		cw.TotalTokens = *data.ContextUsage.Tokens
	}
	s.mu.Lock()
	s.contextWindow = cw
	s.mu.Unlock()
	return cw, nil
}

func (s *session) Close() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.closed)
		if s.stdin != nil {
			err = s.stdin.Close()
		}
		if s.cmd != nil && s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
	})
	return err
}

func (s *session) emit(event agenttypes.Event) {
	s.mu.RLock()
	handler := s.onUpdate
	s.mu.RUnlock()
	if handler != nil {
		handler(event)
	}
}

func (s *session) cachedContextWindow() agenttypes.ContextWindow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.contextWindow
}

func (s *session) refreshState(ctx context.Context) error {
	resp, err := s.request(withDefaultTimeout(ctx), "state", map[string]any{"type": "get_state"})
	if err != nil {
		return err
	}
	var data struct {
		SessionID     string `json:"sessionId"`
		ThinkingLevel string `json:"thinkingLevel"`
		Model         *struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
		} `json:"model"`
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
	if data.Model != nil {
		if id := normalizeModelID(data.Model.Provider + "/" + data.Model.ID); strings.Trim(id, "/") != "" {
			s.model = id
		}
	}
	s.mu.Unlock()
	return nil
}

func withDefaultTimeout(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx
	}
	child, _ := context.WithTimeout(ctx, defaultCommandTimeout)
	return child
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
	case "edit", "write", "write_file":
		return agenttypes.ToolKindEdit
	case "bash", "shell", "run_command":
		return agenttypes.ToolKindExecute
	case "web_search", "search_web":
		return agenttypes.ToolKindWebSearch
	case "ask_user_question":
		return agenttypes.ToolKindAskUser
	default:
		return agenttypes.ToolKindOther
	}
}

func toolTitle(name string, args map[string]any) string {
	name = strings.TrimSpace(name)
	if cmd, ok := args["command"].(string); ok && strings.TrimSpace(cmd) != "" {
		return name + ": " + strings.TrimSpace(cmd)
	}
	if path, ok := args["path"].(string); ok && strings.TrimSpace(path) != "" {
		return name + ": " + filepath.Base(strings.TrimSpace(path))
	}
	return name
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
	}
	if err := json.Unmarshal(raw, &result); err == nil && len(result.Content) > 0 {
		items := make([]agenttypes.ToolCallContentItem, 0, len(result.Content))
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

func preview(text string) string {
	text = strings.TrimSpace(text)
	if len(text) > 160 {
		return text[:160] + "…"
	}
	return text
}
