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
	defaultNodeCommand      = "node"
	defaultCommandTimeout   = 30 * time.Second
	startupTimeout          = 10 * time.Second
	messageEndFallbackDelay = 1500 * time.Millisecond
)

type OpenOptions struct {
	AgentName       string
	SessionKey      string
	Model           string
	Mode            string
	RootPath        string
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
	if _, err := s.request(startCtx, "start", startPayload); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func startPayloadForOptions(opts OpenOptions) map[string]any {
	if scenario := strings.TrimSpace(opts.TestScenario); scenario != "" {
		return map[string]any{"type": "start_test_runtime", "scenario": scenario}
	}
	payload := map[string]any{"type": "start_sdk_runtime"}
	if model := strings.TrimSpace(opts.Model); model != "" {
		payload["model"] = model
	}
	return payload
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

	onUpdate     func(agenttypes.Event)
	sessionID    string
	model        string
	mode         string
	contextStats agenttypes.ContextWindow
	seenText     bool
	seenThinking bool
	lastTurnErr  string
	turn         agenttypes.TurnCanceler
	turnMu       sync.Mutex
	turnDone     chan error

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
		s.resetDeltaState()
	case "message_start":
		s.resetDeltaState()
	case "message_update":
		s.handleMessageUpdate(raw)
	case "message_end":
		s.handleMessageEnd(raw)
	case "context_window":
		s.handleContextWindow(raw)
	case "agent_end":
		s.handleAgentEnd()
	case "tool_execution_start":
		s.handleToolExecutionStart(raw)
	case "tool_execution_update":
		s.handleToolExecutionUpdate(raw)
	case "tool_execution_end":
		s.handleToolExecutionEnd(raw)
	case "recovery", "auto_retry_start":
		s.handleRecovery(raw)
	case "queue_update", "turn_start", "turn_end", "session_info_changed", "thinking_level_changed":
	default:
		log.Printf("[agent/pi-sdk] stdout.ignored_event session=%s type=%q", s.sessionKey, eventType)
	}
}

func (s *session) resetDeltaState() {
	s.mu.Lock()
	s.seenText = false
	s.seenThinking = false
	s.lastTurnErr = ""
	s.mu.Unlock()
}

func (s *session) markTextSeen() {
	s.mu.Lock()
	s.seenText = true
	s.mu.Unlock()
}

func (s *session) markThinkingSeen() {
	s.mu.Lock()
	s.seenThinking = true
	s.mu.Unlock()
}

func (s *session) hasTextSeen() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.seenText
}

func (s *session) hasThinkingSeen() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.seenThinking
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
		if ev.AssistantMessageEvent.Delta != "" {
			s.markTextSeen()
			s.emit(agenttypes.Event{Type: agenttypes.EventTypeMessageChunk, SessionID: s.SessionID(), Data: agenttypes.MessageChunk{Content: ev.AssistantMessageEvent.Delta}})
			return
		}
	case "thinking_delta":
		if ev.AssistantMessageEvent.Delta != "" {
			s.markThinkingSeen()
			s.emit(agenttypes.Event{Type: agenttypes.EventTypeThoughtChunk, SessionID: s.SessionID(), Data: agenttypes.ThoughtChunk{Content: ev.AssistantMessageEvent.Delta}})
			return
		}
	case "text_end":
		if text := strings.TrimSpace(ev.AssistantMessageEvent.Content); text != "" && !s.hasTextSeen() {
			s.markTextSeen()
			s.emit(agenttypes.Event{Type: agenttypes.EventTypeMessageChunk, SessionID: s.SessionID(), Data: agenttypes.MessageChunk{Content: text}})
			return
		}
	case "thinking_end":
		thinking := strings.TrimSpace(ev.AssistantMessageEvent.Thinking)
		if thinking == "" {
			thinking = strings.TrimSpace(ev.AssistantMessageEvent.Content)
		}
		if thinking != "" && !s.hasThinkingSeen() {
			s.markThinkingSeen()
			s.emit(agenttypes.Event{Type: agenttypes.EventTypeThoughtChunk, SessionID: s.SessionID(), Data: agenttypes.ThoughtChunk{Content: thinking}})
			return
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
	for _, item := range ev.Message.Content {
		switch item.Type {
		case "text":
			text := strings.TrimSpace(item.Text)
			if text != "" && !s.hasTextSeen() {
				s.markTextSeen()
				s.emit(agenttypes.Event{Type: agenttypes.EventTypeMessageChunk, SessionID: s.SessionID(), Data: agenttypes.MessageChunk{Content: text}})
			}
		case "thinking":
			thinking := strings.TrimSpace(item.Thinking)
			if thinking != "" && !s.hasThinkingSeen() {
				s.markThinkingSeen()
				s.emit(agenttypes.Event{Type: agenttypes.EventTypeThoughtChunk, SessionID: s.SessionID(), Data: agenttypes.ThoughtChunk{Content: thinking}})
			}
		}
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
	s.scheduleMessageEndFallback()
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

func (s *session) handleAgentEnd() {
	contextWindow := s.cachedContextWindow()
	s.emit(agenttypes.Event{Type: agenttypes.EventTypeMessageDone, SessionID: s.SessionID(), Data: agenttypes.MessageDone{ContextWindow: contextWindow}})
	s.mu.RLock()
	lastErr := strings.TrimSpace(s.lastTurnErr)
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
		RawType: "pi-sdk",
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
		RawType: "pi-sdk",
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
		RawType: "pi-sdk",
		Meta:    resultMeta(nil, ev.Result),
	}})
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

func (s *session) scheduleMessageEndFallback() {
	s.turnMu.Lock()
	ch := s.turnDone
	s.turnMu.Unlock()
	go func() {
		time.Sleep(messageEndFallbackDelay)
		s.signalTurnDoneTo(ch, nil)
	}()
}

func (s *session) resetTurnDone() chan error {
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	s.turnDone = make(chan error, 1)
	return s.turnDone
}

func (s *session) emit(ev agenttypes.Event) {
	s.mu.RLock()
	onUpdate := s.onUpdate
	s.mu.RUnlock()
	if onUpdate != nil {
		onUpdate(ev)
	}
}

func (s *session) SendMessage(ctx context.Context, content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	turnCtx, turnID := s.turn.Begin(ctx)
	defer s.turn.End(turnID)
	turnDone := s.resetTurnDone()
	s.resetDeltaState()
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

func (s *session) AnswerQuestion(context.Context, agenttypes.AskUserAnswer) error {
	return errors.New("ask user question is not supported by pi-sdk runtime foundation")
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
	log.Printf("[agent/pi-sdk] commands.cached session=%s count=%d", s.sessionKey, len(commands))
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

func (s *session) ContextWindow(context.Context) (agenttypes.ContextWindow, error) {
	return s.cachedContextWindow(), nil
}

func (s *session) cachedContextWindow() agenttypes.ContextWindow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.contextStats
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
	return s[:max] + "..."
}
