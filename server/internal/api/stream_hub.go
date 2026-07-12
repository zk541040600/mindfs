package api

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	agenttypes "mindfs/server/internal/agent/types"
	"mindfs/server/internal/api/usecase"
	"mindfs/server/internal/e2ee"
	"mindfs/server/internal/notify"
	"mindfs/server/internal/session"

	"github.com/gorilla/websocket"
)

type StreamHub struct {
	mu              sync.RWMutex
	e2eeManager     *e2ee.Manager
	clients         map[string]*websocket.Conn
	connLocks       map[*websocket.Conn]*sync.Mutex
	sessionClients  map[string]map[string]struct{}
	pendingSessions map[string]*SessionPendingState
	replayStates    map[string]*ClientReplayState
	activeRequests  map[string]string
	completed       map[string]*CompletedSessionState
}

type PendingUserMessage struct {
	Agent       string    `json:"agent,omitempty"`
	Model       string    `json:"model,omitempty"`
	Mode        string    `json:"mode,omitempty"`
	Effort      string    `json:"effort,omitempty"`
	FastService string    `json:"fast_service,omitempty"`
	PlanMode    bool      `json:"plan_mode,omitempty"`
	Content     string    `json:"content"`
	Timestamp   time.Time `json:"timestamp"`
}

type QueuedUserMessage struct {
	ID string `json:"id"`
	PendingUserMessage
	ClientCtx usecase.ClientContext `json:"-"`
}

type SessionPendingState struct {
	RootID       string
	SessionTitle string
	Active       bool
	QueueFrozen  bool
	User         *PendingUserMessage
	Queue        []QueuedUserMessage
	ReplyingList []StreamEvent
	Summary      string
	UpdatedAt    time.Time
}

type ClientStreamStatus string

const (
	ClientStreamStatusReplay ClientStreamStatus = "replay"
	ClientStreamStatusLive   ClientStreamStatus = "live"
)

type ClientReplayState struct {
	Status      ClientStreamStatus
	ReplayIndex int
	RequestID   string
}

type CompletedSessionState struct {
	RequestID    string
	ErrorCode    string
	ErrorMessage string
	Cancelled    bool
	Completed    time.Time
}

type ReplyingSessionState struct {
	RootID       string    `json:"rootId"`
	SessionKey   string    `json:"sessionKey"`
	SessionTitle string    `json:"sessionTitle"`
	Status       string    `json:"status"`
	Summary      string    `json:"summary"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type PendingSessionSnapshot struct {
	RootID       string
	SessionTitle string
	Summary      string
	UpdatedAt    time.Time
}

type replayStep struct {
	events []StreamEvent
	live   bool
	valid  bool
}

var clearSessionPendingReplayWait = 2 * time.Second

func blank(value string) bool {
	return strings.TrimSpace(value) == ""
}

func NewStreamHub(e2eeManager *e2ee.Manager) *StreamHub {
	return &StreamHub{
		e2eeManager:     e2eeManager,
		clients:         make(map[string]*websocket.Conn),
		connLocks:       make(map[*websocket.Conn]*sync.Mutex),
		sessionClients:  make(map[string]map[string]struct{}),
		pendingSessions: make(map[string]*SessionPendingState),
		replayStates:    make(map[string]*ClientReplayState),
		activeRequests:  make(map[string]string),
		completed:       make(map[string]*CompletedSessionState),
	}
}

func pendingClientKey(clientID, sessionKey string) string {
	return clientID + "::" + sessionKey
}

func cloneEvent(ev StreamEvent) StreamEvent {
	return StreamEvent{Type: ev.Type, Data: ev.Data}
}

func cloneUserExchange(msg *PendingUserMessage) *session.Exchange {
	if msg == nil {
		return nil
	}
	return &session.Exchange{
		Role:        "user",
		Agent:       msg.Agent,
		Model:       msg.Model,
		Mode:        msg.Mode,
		Effort:      msg.Effort,
		FastService: msg.FastService,
		Content:     msg.Content,
		Timestamp:   msg.Timestamp,
	}
}

func buildSessionStreamResponse(rootID, sessionKey, requestID string, event *StreamEvent) WSResponse {
	payload := map[string]any{
		"root_id":     rootID,
		"session_key": sessionKey,
		"event":       event,
	}
	if strings.TrimSpace(requestID) != "" {
		payload["request_id"] = requestID
	}
	return WSResponse{
		Type:    "session.stream",
		Payload: payload,
	}
}

func buildSessionDoneResponse(rootID, sessionKey, requestID string, replay bool) WSResponse {
	payload := map[string]any{
		"root_id":     rootID,
		"session_key": sessionKey,
	}
	if strings.TrimSpace(requestID) != "" {
		payload["request_id"] = requestID
	}
	if replay {
		payload["replay"] = true
	}
	return WSResponse{
		ID:      requestID,
		Type:    "session.done",
		Payload: payload,
	}
}

func buildSessionCancelledResponse(responseID, rootID, sessionKey, requestID string, replay bool) WSResponse {
	payload := map[string]any{
		"root_id":     rootID,
		"session_key": sessionKey,
	}
	if strings.TrimSpace(requestID) != "" {
		payload["request_id"] = requestID
	}
	if replay {
		payload["replay"] = true
	}
	return WSResponse{
		ID:      responseID,
		Type:    "session.cancelled",
		Payload: payload,
	}
}

func buildStaleSessionCancelledResponse(responseID, rootID, sessionKey, requestID string) WSResponse {
	resp := buildSessionCancelledResponse(responseID, rootID, sessionKey, requestID, false)
	resp.Payload["stale"] = true
	return resp
}

func buildSessionErrorResponse(rootID, sessionKey, requestID, code, message string, replay bool) WSResponse {
	payload := map[string]any{
		"root_id":     rootID,
		"session_key": sessionKey,
	}
	if strings.TrimSpace(requestID) != "" {
		payload["request_id"] = requestID
	}
	if replay {
		payload["replay"] = true
	}
	return WSResponse{
		ID:      requestID,
		Type:    "session.error",
		Payload: payload,
		Error: &WSResponseError{
			Code:    code,
			Message: message,
		},
	}
}

func buildSlashCommandStreamResponse(rootID, sessionKey, command, requestID string, event *StreamEvent) WSResponse {
	return WSResponse{
		ID:   requestID,
		Type: "session.slash_command.stream",
		Payload: map[string]any{
			"root_id":     rootID,
			"session_key": sessionKey,
			"command":     command,
			"request_id":  requestID,
			"event":       event,
		},
	}
}

func buildSlashCommandDoneResponse(rootID, sessionKey, command, requestID string) WSResponse {
	return WSResponse{
		ID:   requestID,
		Type: "session.slash_command.done",
		Payload: map[string]any{
			"root_id":     rootID,
			"session_key": sessionKey,
			"command":     command,
			"request_id":  requestID,
		},
	}
}

func buildSessionUserMessageResponse(rootID, sessionKey, requestID, sessionType, sessionName, agentName, model, mode, effort, fastService string, planMode bool, content string, timestamp time.Time, queued bool) WSResponse {
	queueState := "active"
	if queued {
		queueState = "dequeued"
	}
	sessionPayload := map[string]any{
		"key":          sessionKey,
		"type":         sessionType,
		"agent":        agentName,
		"model":        model,
		"mode":         mode,
		"effort":       effort,
		"fast_service": fastService,
		"plan_mode":    planMode,
		"created_at":   timestamp,
		"updated_at":   timestamp,
	}
	if strings.TrimSpace(sessionName) != "" {
		sessionPayload["name"] = sessionName
	}
	payload := map[string]any{
		"root_id":     rootID,
		"session_key": sessionKey,
		"session":     sessionPayload,
		"exchange": map[string]any{
			"role":         "user",
			"agent":        agentName,
			"model":        model,
			"mode":         mode,
			"effort":       effort,
			"fast_service": fastService,
			"content":      content,
			"timestamp":    timestamp,
			"queued":       queued,
			"queue_state":  queueState,
		},
	}
	if strings.TrimSpace(requestID) != "" {
		payload["request_id"] = requestID
	}
	return WSResponse{
		Type:    "session.user_message",
		Payload: payload,
	}
}

func buildSessionQueueUpdatedResponse(rootID, sessionKey, requestID string, queue []QueuedUserMessage, frozen bool) WSResponse {
	payload := map[string]any{
		"root_id":      rootID,
		"session_key":  sessionKey,
		"queue":        queue,
		"queue_frozen": frozen,
	}
	if strings.TrimSpace(requestID) != "" {
		payload["request_id"] = requestID
	}
	return WSResponse{
		Type:    "session.queue.updated",
		Payload: payload,
	}
}

func (h *StreamHub) ensurePendingSessionLocked(sessionKey string) *SessionPendingState {
	state := h.pendingSessions[sessionKey]
	if state == nil {
		state = &SessionPendingState{}
		h.pendingSessions[sessionKey] = state
	}
	return state
}

func (h *StreamHub) clearReplayStatesForSessionLocked(sessionKey string) {
	for _, replayKey := range h.getReplayKeyListLocked(sessionKey, "") {
		delete(h.replayStates, replayKey)
	}
}

func (h *StreamHub) requestMatchesLocked(sessionKey, requestID string) bool {
	current, scoped := h.activeRequests[sessionKey]
	if !scoped || strings.TrimSpace(current) == "" {
		return true
	}
	return strings.TrimSpace(current) == strings.TrimSpace(requestID)
}

func (h *StreamHub) currentRequestID(sessionKey string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.activeRequests[sessionKey]
}

func (h *StreamHub) RegisterClient(clientID string, conn *websocket.Conn) {
	if blank(clientID) || conn == nil {
		return
	}
	h.mu.Lock()
	h.clients[clientID] = conn
	if _, ok := h.connLocks[conn]; !ok {
		h.connLocks[conn] = &sync.Mutex{}
	}
	h.mu.Unlock()
}

func (h *StreamHub) UnregisterClient(clientID string, conn *websocket.Conn) {
	if blank(clientID) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	existing := h.clients[clientID]
	if existing != conn {
		return
	}
	delete(h.clients, clientID)
	delete(h.connLocks, conn)
	for sessionKey, clientSet := range h.sessionClients {
		delete(clientSet, clientID)
		if len(clientSet) == 0 {
			delete(h.sessionClients, sessionKey)
		}
	}
	for _, replayKey := range h.getReplayKeyListLocked("", clientID) {
		delete(h.replayStates, replayKey)
	}
}

func (h *StreamHub) BindSessionClient(sessionKey, clientID string) {
	if blank(sessionKey) || blank(clientID) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[clientID]; !ok {
		return
	}
	clientSet := h.sessionClients[sessionKey]
	if clientSet == nil {
		clientSet = make(map[string]struct{})
		h.sessionClients[sessionKey] = clientSet
	}
	clientSet[clientID] = struct{}{}
}

func (h *StreamHub) GetSessionClientIDs(sessionKey string, liveOnly bool) []string {
	if blank(sessionKey) {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	clientSet := h.sessionClients[sessionKey]
	if len(clientSet) == 0 {
		return nil
	}
	out := make([]string, 0, len(clientSet))
	for clientID := range clientSet {
		if h.clients[clientID] == nil {
			continue
		}
		if liveOnly && h.isReplayClientLocked(clientID, sessionKey) {
			continue
		}
		out = append(out, clientID)
	}
	return out
}

func (h *StreamHub) getAllClientIDs() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if len(h.clients) == 0 {
		return nil
	}
	clientIDs := make([]string, 0, len(h.clients))
	for clientID, conn := range h.clients {
		if conn != nil {
			clientIDs = append(clientIDs, clientID)
		}
	}
	return clientIDs
}

func (h *StreamHub) SetPendingUser(rootID, sessionKey, sessionTitle, agent, model, mode, effort, fastService string, planMode bool, content string) *PendingUserMessage {
	return h.setPendingUserForRequest(rootID, sessionKey, "", sessionTitle, agent, model, mode, effort, fastService, planMode, content)
}

func (h *StreamHub) setPendingUserForRequest(rootID, sessionKey, requestID, sessionTitle, agent, model, mode, effort, fastService string, planMode bool, content string) *PendingUserMessage {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.ensurePendingSessionLocked(sessionKey)
	h.activeRequests[sessionKey] = strings.TrimSpace(requestID)
	delete(h.completed, sessionKey)
	state.RootID = rootID
	state.SessionTitle = strings.TrimSpace(sessionTitle)
	state.Active = true
	state.User = &PendingUserMessage{
		Agent:       agent,
		Model:       model,
		Mode:        mode,
		Effort:      effort,
		FastService: fastService,
		PlanMode:    planMode,
		Content:     content,
		Timestamp:   time.Now().UTC(),
	}
	state.ReplyingList = nil
	state.Summary = ""
	state.UpdatedAt = state.User.Timestamp
	h.clearReplayStatesForSessionLocked(sessionKey)
	return &PendingUserMessage{
		Agent:       state.User.Agent,
		Model:       state.User.Model,
		Mode:        state.User.Mode,
		Effort:      state.User.Effort,
		FastService: state.User.FastService,
		PlanMode:    state.User.PlanMode,
		Content:     state.User.Content,
		Timestamp:   state.User.Timestamp,
	}
}

func (h *StreamHub) IsSessionReplying(sessionKey string) bool {
	if blank(sessionKey) {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	state := h.pendingSessions[sessionKey]
	return state != nil && state.Active
}

func cloneQueue(queue []QueuedUserMessage) []QueuedUserMessage {
	if len(queue) == 0 {
		return nil
	}
	out := make([]QueuedUserMessage, len(queue))
	copy(out, queue)
	return out
}

func (h *StreamHub) queueSnapshot(sessionKey string) (string, []QueuedUserMessage, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	state := h.pendingSessions[sessionKey]
	if state == nil {
		return "", nil, false
	}
	return state.RootID, cloneQueue(state.Queue), state.QueueFrozen
}

func (h *StreamHub) EnqueueSessionMessage(rootID, sessionKey, sessionTitle string, item QueuedUserMessage) []QueuedUserMessage {
	if item.ID == "" {
		item.ID = time.Now().UTC().Format("20060102150405.000000000")
	}
	if item.Timestamp.IsZero() {
		item.Timestamp = time.Now().UTC()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.ensurePendingSessionLocked(sessionKey)
	delete(h.completed, sessionKey)
	state.RootID = rootID
	if strings.TrimSpace(sessionTitle) != "" {
		state.SessionTitle = strings.TrimSpace(sessionTitle)
	}
	state.Queue = append(state.Queue, item)
	state.UpdatedAt = item.Timestamp
	h.clearReplayStatesForSessionLocked(sessionKey)
	return cloneQueue(state.Queue)
}

func (h *StreamHub) RemoveQueuedSessionMessage(sessionKey, queueID string) []QueuedUserMessage {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.pendingSessions[sessionKey]
	if state == nil || queueID == "" {
		return nil
	}
	next := state.Queue[:0]
	for _, item := range state.Queue {
		if item.ID == queueID {
			continue
		}
		next = append(next, item)
	}
	state.Queue = next
	state.UpdatedAt = time.Now().UTC()
	h.clearReplayStatesForSessionLocked(sessionKey)
	return cloneQueue(state.Queue)
}

func (h *StreamHub) UpdateQueuedSessionMessage(sessionKey, queueID, content string) []QueuedUserMessage {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.pendingSessions[sessionKey]
	if state == nil || queueID == "" {
		return nil
	}
	for i := range state.Queue {
		if state.Queue[i].ID == queueID {
			state.Queue[i].Content = content
			break
		}
	}
	state.UpdatedAt = time.Now().UTC()
	h.clearReplayStatesForSessionLocked(sessionKey)
	return cloneQueue(state.Queue)
}

func (h *StreamHub) FreezeQueuedSessionMessages(sessionKey string) ([]QueuedUserMessage, bool) {
	if blank(sessionKey) {
		return nil, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.pendingSessions[sessionKey]
	if state == nil || len(state.Queue) == 0 {
		return nil, false
	}
	state.QueueFrozen = true
	state.UpdatedAt = time.Now().UTC()
	return cloneQueue(state.Queue), true
}

func (h *StreamHub) UnfreezeQueuedSessionMessages(sessionKey string) ([]QueuedUserMessage, bool) {
	if blank(sessionKey) {
		return nil, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.pendingSessions[sessionKey]
	if state == nil || !state.QueueFrozen {
		if state == nil {
			return nil, false
		}
		return cloneQueue(state.Queue), false
	}
	state.QueueFrozen = false
	state.UpdatedAt = time.Now().UTC()
	return cloneQueue(state.Queue), true
}

func (h *StreamHub) PopQueuedSessionMessage(sessionKey, queueID string) (QueuedUserMessage, []QueuedUserMessage, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.pendingSessions[sessionKey]
	if state == nil || len(state.Queue) == 0 {
		return QueuedUserMessage{}, nil, false
	}
	index := 0
	if trimmedQueueID := strings.TrimSpace(queueID); trimmedQueueID != "" {
		index = -1
		for i := range state.Queue {
			if state.Queue[i].ID == trimmedQueueID {
				index = i
				break
			}
		}
		if index < 0 {
			return QueuedUserMessage{}, cloneQueue(state.Queue), false
		}
	} else if state.QueueFrozen {
		return QueuedUserMessage{}, cloneQueue(state.Queue), false
	}
	item := state.Queue[index]
	state.Queue = append(state.Queue[:index], state.Queue[index+1:]...)
	state.UpdatedAt = time.Now().UTC()
	h.clearReplayStatesForSessionLocked(sessionKey)
	return item, cloneQueue(state.Queue), true
}

func (h *StreamHub) PromoteQueuedSessionMessage(sessionKey, queueID string) ([]QueuedUserMessage, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.pendingSessions[sessionKey]
	if state == nil || len(state.Queue) == 0 || strings.TrimSpace(queueID) == "" {
		return nil, false
	}
	index := -1
	for i := range state.Queue {
		if state.Queue[i].ID == queueID {
			index = i
			break
		}
	}
	if index < 0 {
		return cloneQueue(state.Queue), false
	}
	if index > 0 {
		item := state.Queue[index]
		copy(state.Queue[1:index+1], state.Queue[0:index])
		state.Queue[0] = item
	}
	state.QueueFrozen = false
	state.UpdatedAt = time.Now().UTC()
	h.clearReplayStatesForSessionLocked(sessionKey)
	return cloneQueue(state.Queue), true
}

func (h *StreamHub) SetPendingReply(rootID, sessionKey, sessionTitle string) {
	if blank(sessionKey) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.ensurePendingSessionLocked(sessionKey)
	h.activeRequests[sessionKey] = ""
	delete(h.completed, sessionKey)
	state.RootID = rootID
	state.SessionTitle = strings.TrimSpace(sessionTitle)
	state.Active = true
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now().UTC()
	}
	h.clearReplayStatesForSessionLocked(sessionKey)
}

func (h *StreamHub) GetPendingUserExchange(sessionKey string) *session.Exchange {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.pendingSessions[sessionKey]
	if state == nil {
		return nil
	}
	return cloneUserExchange(state.User)
}

func (h *StreamHub) PendingSessionSnapshot(sessionKey string) PendingSessionSnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	state := h.pendingSessions[sessionKey]
	if state == nil {
		return PendingSessionSnapshot{}
	}
	return PendingSessionSnapshot{
		RootID:       state.RootID,
		SessionTitle: state.SessionTitle,
		Summary:      state.Summary,
		UpdatedAt:    state.UpdatedAt,
	}
}

func (h *StreamHub) AppendReplyEvent(sessionKey string, event StreamEvent) bool {
	return h.appendReplyEventForRequest(sessionKey, "", event)
}

func (h *StreamHub) appendReplyEventForRequest(sessionKey, requestID string, event StreamEvent) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.requestMatchesLocked(sessionKey, requestID) {
		return false
	}
	if h.completed[sessionKey] != nil {
		return false
	}
	state := h.ensurePendingSessionLocked(sessionKey)
	if coalesceGoalStateStreamEvent(state, event) {
		state.UpdatedAt = time.Now().UTC()
		return true
	}
	if coalesceUserShellStreamEvent(state, event) {
		state.UpdatedAt = time.Now().UTC()
		return true
	}
	state.ReplyingList = append(state.ReplyingList, cloneEvent(event))
	state.UpdatedAt = time.Now().UTC()
	if event.Type == "message_chunk" {
		if chunk, ok := event.Data.(agenttypes.MessageChunk); ok {
			state.Summary = lastRunes(state.Summary+chunk.Content, notify.BodyMaxRunes)
		}
	} else if isAuxiliarySummaryBoundary(event.Type) {
		state.Summary = ""
	}
	return true
}

func isAuxiliarySummaryBoundary(eventType string) bool {
	switch agenttypes.EventType(eventType) {
	case agenttypes.EventTypeThoughtChunk,
		agenttypes.EventTypeToolCall,
		agenttypes.EventTypeToolUpdate,
		agenttypes.EventTypeTodoUpdate,
		agenttypes.EventTypePlanUpdate,
		agenttypes.EventTypeCompact,
		agenttypes.EventTypeGoalState:
		return true
	default:
		return false
	}
}

func coalesceGoalStateStreamEvent(state *SessionPendingState, event StreamEvent) bool {
	if state == nil || event.Type != string(agenttypes.EventTypeGoalState) {
		return false
	}
	for i := len(state.ReplyingList) - 1; i >= 0; i-- {
		if state.ReplyingList[i].Type != event.Type {
			continue
		}
		state.ReplyingList[i] = cloneEvent(event)
		return true
	}
	return false
}

const maxReplayUserShellStreamBytes = 256 * 1024

func coalesceUserShellStreamEvent(state *SessionPendingState, event StreamEvent) bool {
	if state == nil || event.Type != string(agenttypes.EventTypeToolUpdate) {
		return false
	}
	next, ok := event.Data.(agenttypes.ToolCall)
	if !ok || next.Meta == nil || next.Meta["source"] != "userShell" || next.Meta["phase"] != "stream" || next.CallID == "" {
		return false
	}
	for i := len(state.ReplyingList) - 1; i >= 0; i-- {
		prevEvent := state.ReplyingList[i]
		if prevEvent.Type != event.Type {
			continue
		}
		prev, ok := prevEvent.Data.(agenttypes.ToolCall)
		if !ok || prev.CallID != next.CallID || prev.Meta == nil || prev.Meta["source"] != "userShell" || prev.Meta["phase"] != "stream" {
			continue
		}
		merged := prev
		merged.Status = next.Status
		merged.Meta = map[string]any{}
		for key, value := range prev.Meta {
			merged.Meta[key] = value
		}
		for key, value := range next.Meta {
			merged.Meta[key] = value
		}
		text := toolCallText(prev.Content) + toolCallText(next.Content)
		if len(text) > maxReplayUserShellStreamBytes {
			text = text[len(text)-maxReplayUserShellStreamBytes:]
			merged.Meta["replayTruncated"] = true
			merged.Meta["replayTruncation"] = "tail"
		}
		merged.Content = []agenttypes.ToolCallContentItem{{Type: "text", Text: text}}
		state.ReplyingList[i] = StreamEvent{Type: event.Type, Data: merged}
		return true
	}
	return false
}

func toolCallText(items []agenttypes.ToolCallContentItem) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	for _, item := range items {
		b.WriteString(item.Text)
	}
	return b.String()
}

func (h *StreamHub) ListReplyingSessions() []ReplyingSessionState {
	h.mu.RLock()
	defer h.mu.RUnlock()
	items := make([]ReplyingSessionState, 0, len(h.pendingSessions))
	for sessionKey, state := range h.pendingSessions {
		if state == nil || !state.Active || blank(sessionKey) || blank(state.RootID) {
			continue
		}
		items = append(items, ReplyingSessionState{
			RootID:       state.RootID,
			SessionKey:   sessionKey,
			SessionTitle: state.SessionTitle,
			Status:       "replying",
			Summary:      state.Summary,
			UpdatedAt:    state.UpdatedAt,
		})
	}
	return items
}

func (h *StreamHub) ReplayPending(rootID, clientID, sessionKey string) {
	h.mu.Lock()
	requestID := h.activeRequests[sessionKey]
	h.replayStates[pendingClientKey(clientID, sessionKey)] = &ClientReplayState{
		Status:      ClientStreamStatusReplay,
		ReplayIndex: 0,
		RequestID:   requestID,
	}
	h.mu.Unlock()

	h.replayQueueToClient(rootID, clientID, sessionKey, requestID)
	for {
		step := h.collectReplayStep(clientID, sessionKey, requestID)
		if !step.valid {
			return
		}
		if !h.replayStepToClient(rootID, clientID, sessionKey, requestID, step.events) {
			return
		}
		if step.live {
			h.replayCompletionToClient(rootID, clientID, sessionKey, requestID)
			return
		}
	}
}

func (h *StreamHub) HasReplayClients(rootID, sessionKey string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, replayKey := range h.getReplayKeyListLocked(sessionKey, "") {
		replay := h.replayStates[replayKey]
		if replay != nil && replay.Status == ClientStreamStatusReplay {
			return true
		}
	}
	return false
}

func (h *StreamHub) ClearSessionPending(sessionKey string) {
	h.clearSessionPendingForRequest(sessionKey, "")
}

func (h *StreamHub) clearSessionPendingForRequest(sessionKey, requestID string) bool {
	if blank(sessionKey) {
		return false
	}
	deadline := time.Now().Add(clearSessionPendingReplayWait)
	for h.HasReplayClients("", sessionKey) {
		if clearSessionPendingReplayWait <= 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.requestMatchesLocked(sessionKey, requestID) {
		return false
	}
	state := h.pendingSessions[sessionKey]
	if state != nil && len(state.Queue) > 0 {
		state.Active = false
		state.User = nil
		state.ReplyingList = nil
		state.Summary = ""
		state.UpdatedAt = time.Now().UTC()
	} else {
		delete(h.pendingSessions, sessionKey)
	}
	h.clearReplayStatesForSessionLocked(sessionKey)
	return true
}

func (h *StreamHub) SendToClient(clientID string, resp WSResponse) {
	if blank(clientID) {
		return
	}
	h.mu.RLock()
	conn := h.clients[clientID]
	h.mu.RUnlock()
	if conn == nil {
		return
	}
	_ = h.WriteJSON(clientID, conn, resp)
}

func (h *StreamHub) BroadcastAll(resp WSResponse) {
	for _, clientID := range h.getAllClientIDs() {
		h.SendToClient(clientID, resp)
	}
}

func (h *StreamHub) BroadcastSessionStream(rootID, sessionKey string, event *StreamEvent) {
	h.broadcastSessionStreamForRequest(rootID, sessionKey, "", event)
}

func (h *StreamHub) broadcastSessionStreamForRequest(rootID, sessionKey, requestID string, event *StreamEvent) bool {
	if event == nil {
		return false
	}
	if !h.appendReplyEventForRequest(sessionKey, requestID, *event) {
		return false
	}
	for _, clientID := range h.GetSessionClientIDs(sessionKey, true) {
		resp := buildSessionStreamResponse(rootID, sessionKey, requestID, event)
		h.SendToClient(clientID, resp)
	}
	return true
}

func (h *StreamHub) recordSessionTerminal(sessionKey string, terminal CompletedSessionState) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.requestMatchesLocked(sessionKey, terminal.RequestID) {
		return false
	}
	if h.terminalAlreadyRecordedLocked(sessionKey, terminal.RequestID) {
		return false
	}
	terminal.Completed = time.Now().UTC()
	h.completed[sessionKey] = &terminal
	return true
}

func (h *StreamHub) BroadcastSessionDone(rootID, sessionKey, requestID string) {
	if !h.recordSessionTerminal(sessionKey, CompletedSessionState{RequestID: requestID}) {
		return
	}
	h.sendSessionDone(rootID, sessionKey, requestID)
}

func (h *StreamHub) sendSessionDone(rootID, sessionKey, requestID string) {
	resp := buildSessionDoneResponse(rootID, sessionKey, requestID, false)
	for _, clientID := range h.GetSessionClientIDs(sessionKey, false) {
		h.SendToClient(clientID, resp)
	}
}

func (h *StreamHub) BroadcastSessionCancelled(rootID, sessionKey, responseID, requestID string) {
	if !h.recordSessionTerminal(sessionKey, CompletedSessionState{
		RequestID: requestID,
		Cancelled: true,
	}) {
		return
	}
	if !h.clearSessionPendingForRequest(sessionKey, requestID) {
		return
	}
	resp := buildSessionCancelledResponse(responseID, rootID, sessionKey, requestID, false)
	for _, clientID := range h.GetSessionClientIDs(sessionKey, false) {
		h.SendToClient(clientID, resp)
	}
}

func (h *StreamHub) BroadcastSessionError(rootID, sessionKey, requestID, code, message string) {
	if !h.recordSessionTerminal(sessionKey, CompletedSessionState{
		RequestID:    requestID,
		ErrorCode:    code,
		ErrorMessage: message,
	}) {
		return
	}
	h.sendSessionError(rootID, sessionKey, requestID, code, message)
}

func (h *StreamHub) sendSessionError(rootID, sessionKey, requestID, code, message string) {
	resp := buildSessionErrorResponse(rootID, sessionKey, requestID, code, message, false)
	for _, clientID := range h.GetSessionClientIDs(sessionKey, false) {
		h.SendToClient(clientID, resp)
	}
}

func (h *StreamHub) terminalAlreadyRecordedLocked(sessionKey, requestID string) bool {
	completed := h.completed[sessionKey]
	if completed == nil {
		return false
	}
	return strings.TrimSpace(completed.RequestID) == strings.TrimSpace(requestID)
}

func (h *StreamHub) BroadcastSessionUserMessage(
	rootID string,
	sessionKey string,
	sessionType string,
	sessionName string,
	agentName string,
	model string,
	mode string,
	effort string,
	fastService string,
	planMode bool,
	content string,
	excludeClientID string,
	queued bool,
) {
	h.broadcastSessionUserMessageForRequest(rootID, sessionKey, "", sessionType, sessionName, agentName, model, mode, effort, fastService, planMode, content, excludeClientID, queued)
}

func (h *StreamHub) broadcastSessionUserMessageForRequest(
	rootID string,
	sessionKey string,
	requestID string,
	sessionType string,
	sessionName string,
	agentName string,
	model string,
	mode string,
	effort string,
	fastService string,
	planMode bool,
	content string,
	excludeClientID string,
	queued bool,
) {
	pendingUser := h.setPendingUserForRequest(rootID, sessionKey, requestID, sessionName, agentName, model, mode, effort, fastService, planMode, content)
	resp := buildSessionUserMessageResponse(rootID, sessionKey, requestID, sessionType, sessionName, agentName, model, mode, effort, fastService, planMode, content, pendingUser.Timestamp, queued)
	for _, clientID := range h.GetSessionClientIDs(sessionKey, false) {
		if clientID == excludeClientID {
			continue
		}
		h.SendToClient(clientID, resp)
	}
}

func (h *StreamHub) BroadcastSessionQueueUpdated(rootID, sessionKey string, queue []QueuedUserMessage) {
	_, _, frozen := h.queueSnapshot(sessionKey)
	resp := buildSessionQueueUpdatedResponse(rootID, sessionKey, "", queue, frozen)
	for _, clientID := range h.GetSessionClientIDs(sessionKey, false) {
		h.SendToClient(clientID, resp)
	}
}

func lastRunes(value string, max int) string {
	if max <= 0 || value == "" {
		return ""
	}
	if utf8.RuneCountInString(value) <= max {
		return value
	}
	runes := []rune(value)
	return "..." + string(runes[len(runes)-max:])
}

func (h *StreamHub) WriteJSON(clientID string, conn *websocket.Conn, value any) error {
	if conn == nil {
		return nil
	}
	lock := h.getConnLock(conn)
	lock.Lock()
	defer lock.Unlock()
	if h.e2eeManager != nil && h.e2eeManager.Enabled() {
		if resp, ok := value.(WSResponse); ok && resp.Type == "e2ee.error" {
			return conn.WriteJSON(resp)
		}
		sess, err := h.e2eeManager.SessionForClient(clientID)
		if err != nil {
			return nil
		}
		payload, err := json.Marshal(value)
		if err != nil {
			return err
		}
		envelope, err := e2ee.EncryptBytes(sess.Key, payload)
		if err != nil {
			return err
		}
		return conn.WriteJSON(envelope)
	}
	return conn.WriteJSON(value)
}

func (h *StreamHub) getConnLock(conn *websocket.Conn) *sync.Mutex {
	h.mu.RLock()
	lock := h.connLocks[conn]
	h.mu.RUnlock()
	if lock != nil {
		return lock
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing := h.connLocks[conn]; existing != nil {
		return existing
	}
	created := &sync.Mutex{}
	h.connLocks[conn] = created
	return created
}

func (h *StreamHub) collectReplayStep(clientID, sessionKey, requestID string) replayStep {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.nextReplayStepLocked(clientID, sessionKey, requestID)
}

func (h *StreamHub) nextReplayStepLocked(clientID, sessionKey, requestID string) replayStep {
	clientKey := pendingClientKey(clientID, sessionKey)
	replay := h.replayStates[clientKey]
	if replay == nil || replay.RequestID != requestID || !h.requestMatchesLocked(sessionKey, requestID) {
		return replayStep{}
	}
	state := h.pendingSessions[sessionKey]
	if state == nil {
		replay.Status = ClientStreamStatusLive
		return replayStep{live: true, valid: true}
	}
	if replay.ReplayIndex >= len(state.ReplyingList) {
		replay.Status = ClientStreamStatusLive
		return replayStep{live: true, valid: true}
	}
	start := replay.ReplayIndex
	end := len(state.ReplyingList)
	events := append([]StreamEvent(nil), state.ReplyingList[start:end]...)
	replay.ReplayIndex = end
	return replayStep{events: events, valid: true}
}

func (h *StreamHub) replayRequestCurrent(clientID, sessionKey, requestID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	replay := h.replayStates[pendingClientKey(clientID, sessionKey)]
	return replay != nil && replay.RequestID == requestID && h.requestMatchesLocked(sessionKey, requestID)
}

func (h *StreamHub) replayStepToClient(rootID, clientID, sessionKey, requestID string, events []StreamEvent) bool {
	for i := range events {
		if !h.replayRequestCurrent(clientID, sessionKey, requestID) {
			return false
		}
		h.SendToClient(clientID, buildSessionStreamResponse(rootID, sessionKey, requestID, &events[i]))
	}
	return true
}

func (h *StreamHub) replayQueueToClient(rootID, clientID, sessionKey, requestID string) bool {
	stateRoot, queue, frozen := h.queueSnapshot(sessionKey)
	if rootID == "" {
		rootID = stateRoot
	}
	if rootID == "" || len(queue) == 0 {
		return true
	}
	if !h.replayRequestCurrent(clientID, sessionKey, requestID) {
		return false
	}
	h.SendToClient(clientID, buildSessionQueueUpdatedResponse(rootID, sessionKey, requestID, queue, frozen))
	return true
}

func (h *StreamHub) replayCompletionToClient(rootID, clientID, sessionKey, replayRequestID string) {
	if blank(rootID) || blank(clientID) || blank(sessionKey) {
		return
	}
	h.mu.RLock()
	if !h.requestMatchesLocked(sessionKey, replayRequestID) {
		h.mu.RUnlock()
		return
	}
	completed := h.completed[sessionKey]
	if completed == nil {
		h.mu.RUnlock()
		return
	}
	requestID := completed.RequestID
	if strings.TrimSpace(replayRequestID) != "" && strings.TrimSpace(requestID) != strings.TrimSpace(replayRequestID) {
		h.mu.RUnlock()
		return
	}
	errorCode := completed.ErrorCode
	errorMessage := completed.ErrorMessage
	cancelled := completed.Cancelled
	h.mu.RUnlock()
	if strings.TrimSpace(errorMessage) != "" {
		h.SendToClient(clientID, buildSessionErrorResponse(rootID, sessionKey, requestID, errorCode, errorMessage, true))
		return
	}
	if cancelled {
		h.SendToClient(clientID, buildSessionCancelledResponse(requestID, rootID, sessionKey, requestID, true))
		return
	}
	h.SendToClient(clientID, buildSessionDoneResponse(rootID, sessionKey, requestID, true))
}

func (h *StreamHub) isReplayClientLocked(clientID, sessionKey string) bool {
	for _, replayKey := range h.getReplayKeyListLocked(sessionKey, clientID) {
		state := h.replayStates[replayKey]
		return state != nil && state.Status != ClientStreamStatusLive
	}
	return false
}

func (h *StreamHub) getReplayKeyListLocked(sessionKey, clientID string) []string {
	if len(h.replayStates) == 0 {
		return nil
	}
	keys := make([]string, 0, len(h.replayStates))
	for replayKey := range h.replayStates {
		if sessionKey != "" && !strings.HasSuffix(replayKey, "::"+sessionKey) {
			continue
		}
		if clientID != "" && !strings.HasPrefix(replayKey, clientID+"::") {
			continue
		}
		keys = append(keys, replayKey)
	}
	return keys
}
