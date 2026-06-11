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
	defaultNodeCommand = "node"
	startupTimeout     = 10 * time.Second
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
		closed:             make(chan struct{}),
	}
	r.register(s)
	go s.readLoop(stdout)
	go s.stderrLoop(stderr)
	go s.waitLoop()

	startCtx, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()
	if _, err := s.request(startCtx, "start", map[string]any{"type": "start_test_runtime", "scenario": "extension-ui"}); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
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
	turn         agenttypes.TurnCanceler

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
		log.Printf("[agent/pi-sdk] stdout.ignored_event session=%s type=%q", s.sessionKey, envelope.Type)
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
	if content != "/ui-demo" {
		return errors.New("pi sdk runtime foundation supports only /ui-demo")
	}
	turnCtx, turnID := s.turn.Begin(ctx)
	defer s.turn.End(turnID)
	if _, err := s.request(turnCtx, "prompt", map[string]any{"type": "prompt", "message": content}); err != nil {
		return err
	}
	s.emit(agenttypes.Event{Type: agenttypes.EventTypeMessageDone, SessionID: s.SessionID(), Data: agenttypes.MessageDone{ContextWindow: s.cachedContextWindow()}})
	return nil
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

func (s *session) SetModel(context.Context, string) error {
	return errors.New("model switching is not supported by pi-sdk runtime foundation")
}

func (s *session) ListModels(context.Context) (agenttypes.ModelList, error) {
	return agenttypes.ModelList{}, errors.New("model listing is not supported by pi-sdk runtime foundation")
}

func (s *session) SetMode(context.Context, string) error {
	return errors.New("mode switching is not supported by pi-sdk runtime foundation")
}

func (s *session) ListModes(context.Context) (agenttypes.ModeList, error) {
	return agenttypes.ModeList{}, errors.New("mode listing is not supported by pi-sdk runtime foundation")
}

func (s *session) ListCommands(context.Context) (agenttypes.CommandList, error) {
	return agenttypes.CommandList{}, errors.New("command listing is not supported by pi-sdk runtime foundation")
}

func (s *session) CancelCurrentTurn() error {
	s.turn.Cancel()
	return errors.New("turn cancellation is not supported by pi-sdk runtime foundation")
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
	return s.cachedContextWindow(), errors.New("context window is not supported by pi-sdk runtime foundation")
}

func (s *session) cachedContextWindow() agenttypes.ContextWindow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.contextStats
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
