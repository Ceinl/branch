package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const minSSEFlushBytes = 4096
const collabHistoryLimit = 256

type collabUser struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

type collabEvent struct {
	Type     string        `json:"type"`
	Path     string        `json:"path"`
	Content  *string       `json:"content,omitempty"`
	Modified string        `json:"modified,omitempty"`
	Version  uint64        `json:"version"`
	User     collabUser    `json:"user"`
	ClientID string        `json:"clientId,omitempty"`
	Cursor   *collabCursor `json:"cursor,omitempty"`
}

type collabCursor struct {
	BlockIndex int `json:"blockIndex"`
	Offset     int `json:"offset"`
}

type collabClient struct {
	clientID string
	user     collabUser
}

type collabRoom struct {
	clients map[chan collabEvent]collabClient
	history []collabEvent
	version uint64
}

type collabHub struct {
	mu    sync.Mutex
	rooms map[string]*collabRoom
}

func newCollabHub() *collabHub {
	return &collabHub{rooms: make(map[string]*collabRoom)}
}

func (h *collabHub) subscribe(path string, clientID string, user authUser) (chan collabEvent, uint64, []collabEvent, func()) {
	ch := make(chan collabEvent, 16)
	publicUser := publicCollabUser(user)
	var peers []collabEvent
	var join collabEvent
	var recipients []chan collabEvent

	h.mu.Lock()
	room := h.roomLocked(path)
	if clientID != "" {
		room.version++
		for other, client := range room.clients {
			if client.clientID == "" || client.clientID == clientID {
				continue
			}
			peers = append(peers, collabEvent{
				Type:     "presence",
				Path:     path,
				Version:  room.version,
				User:     client.user,
				ClientID: client.clientID,
			})
			recipients = append(recipients, other)
		}
		join = collabEvent{
			Type:     "presence",
			Path:     path,
			Version:  room.version,
			User:     publicUser,
			ClientID: clientID,
		}
		room.appendLocked(join)
	}
	room.clients[ch] = collabClient{clientID: clientID, user: publicUser}
	version := room.version
	h.mu.Unlock()

	for _, other := range recipients {
		select {
		case other <- join:
		default:
		}
	}

	return ch, version, peers, func() {
		var event collabEvent
		var clients []chan collabEvent
		h.mu.Lock()
		if room := h.rooms[path]; room != nil {
			client := room.clients[ch]
			delete(room.clients, ch)
			if len(room.clients) == 0 {
				delete(h.rooms, path)
			} else if client.clientID != "" && !room.hasClientLocked(client.clientID) {
				room.version++
				event = collabEvent{
					Type:     "leave",
					Path:     path,
					Version:  room.version,
					User:     client.user,
					ClientID: client.clientID,
				}
				room.appendLocked(event)
				clients = make([]chan collabEvent, 0, len(room.clients))
				for other := range room.clients {
					clients = append(clients, other)
				}
			}
		}
		h.mu.Unlock()
		for _, other := range clients {
			select {
			case other <- event:
			default:
			}
		}
	}
}

func (r *collabRoom) hasClientLocked(clientID string) bool {
	for _, client := range r.clients {
		if client.clientID == clientID {
			return true
		}
	}
	return false
}

func (r *collabRoom) appendLocked(event collabEvent) {
	r.history = append(r.history, event)
	if len(r.history) > collabHistoryLimit {
		copy(r.history, r.history[len(r.history)-collabHistoryLimit:])
		r.history = r.history[:collabHistoryLimit]
	}
}

func (h *collabHub) broadcastUpdate(path string, content string, modified string, user authUser, clientID string) uint64 {
	return h.broadcast(collabEvent{
		Type:     "update",
		Path:     path,
		Content:  &content,
		Modified: modified,
		User:     publicCollabUser(user),
		ClientID: clientID,
	})
}

func (h *collabHub) broadcastDraft(path string, content *string, cursor *collabCursor, user authUser, clientID string) uint64 {
	var eventContent *string
	if content != nil {
		copy := *content
		eventContent = &copy
	}
	return h.broadcast(collabEvent{
		Type:     "draft",
		Path:     path,
		Content:  eventContent,
		User:     publicCollabUser(user),
		ClientID: clientID,
		Cursor:   cursor,
	})
}

func (h *collabHub) broadcast(event collabEvent) uint64 {
	h.mu.Lock()
	room := h.roomLocked(event.Path)
	room.version++
	event.Version = room.version
	room.appendLocked(event)
	clients := make([]chan collabEvent, 0, len(room.clients))
	for ch := range room.clients {
		clients = append(clients, ch)
	}
	h.mu.Unlock()
	for _, ch := range clients {
		select {
		case ch <- event:
		default:
		}
	}
	return event.Version
}

func (h *collabHub) eventsSince(path string, since uint64) (uint64, []collabEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	room := h.roomLocked(path)
	events := make([]collabEvent, 0, len(room.history))
	for _, event := range room.history {
		if event.Version > since {
			events = append(events, event)
		}
	}
	return room.version, events
}

func (h *collabHub) activePaths() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	paths := make([]string, 0, len(h.rooms))
	for path := range h.rooms {
		paths = append(paths, path)
	}
	return paths
}

func (h *collabHub) roomLocked(path string) *collabRoom {
	room := h.rooms[path]
	if room == nil {
		room = &collabRoom{clients: make(map[chan collabEvent]collabClient)}
		h.rooms[path] = room
	}
	return room
}

func (a *app) handleFileStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	rel := r.URL.Query().Get("path")
	full, cleanRel, err := a.resolveExisting(rel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	info, err := os.Stat(full)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if info.IsDir() {
		http.Error(w, "path is a directory", http.StatusBadRequest)
		return
	}
	if info.Size() > maxEditableBytes {
		http.Error(w, "file is too large for collaboration", http.StatusRequestEntityTooLarge)
		return
	}
	data, err := os.ReadFile(full)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !utf8.Valid(data) {
		http.Error(w, "file is not valid UTF-8 text", http.StatusUnsupportedMediaType)
		return
	}

	path := slashPath(cleanRel)
	clientID := r.URL.Query().Get("clientId")
	ch, version, peers, unsubscribe := a.collab.subscribe(path, clientID, userFromRequest(r))
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	content := string(data)
	initial := collabEvent{
		Type:     "snapshot",
		Path:     path,
		Content:  &content,
		Modified: info.ModTime().Format(time.RFC3339),
		Version:  version,
		User:     publicCollabUser(userFromRequest(r)),
		ClientID: clientID,
	}
	writeSSE(w, initial)
	for _, peer := range peers {
		writeSSE(w, peer)
	}
	flusher.Flush()

	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-ch:
			writeSSE(w, event)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func (a *app) handleFileCollab(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		a.handleFileCollabPoll(w, r)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Path     string        `json:"path"`
		ClientID string        `json:"clientId"`
		Content  *string       `json:"content"`
		Cursor   *collabCursor `json:"cursor"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxEditableBytes*2+1024))
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	// Read-only viewers may share their cursor but never push draft content.
	if req.Content != nil && a.readOnly {
		writeError(w, http.StatusForbidden, "server is read-only")
		return
	}
	if req.Content != nil {
		if len(*req.Content) > maxEditableBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "content is too large for the editor")
			return
		}
		if !utf8.ValidString(*req.Content) {
			writeError(w, http.StatusUnsupportedMediaType, "content is not valid UTF-8 text")
			return
		}
	}
	if req.Cursor != nil {
		if req.Cursor.BlockIndex < 0 {
			req.Cursor.BlockIndex = 0
		}
		if req.Cursor.Offset < 0 {
			req.Cursor.Offset = 0
		}
	}

	full, cleanRel, err := a.resolveExisting(req.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	info, err := os.Stat(full)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if info.IsDir() {
		writeError(w, http.StatusBadRequest, "path is a directory")
		return
	}
	if info.Size() > maxEditableBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "file is too large for collaboration")
		return
	}

	path := slashPath(cleanRel)
	version := a.collab.broadcastDraft(path, req.Content, req.Cursor, userFromRequest(r), req.ClientID)
	writeJSON(w, http.StatusOK, map[string]any{"path": path, "version": version})
}

func (a *app) handleFileCollabPoll(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	full, cleanRel, err := a.resolveExisting(rel)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	info, err := os.Stat(full)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if info.IsDir() {
		writeError(w, http.StatusBadRequest, "path is a directory")
		return
	}
	if info.Size() > maxEditableBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "file is too large for collaboration")
		return
	}

	var since uint64
	if rawSince := r.URL.Query().Get("since"); rawSince != "" {
		parsed, err := strconv.ParseUint(rawSince, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid since version")
			return
		}
		since = parsed
	}

	path := slashPath(cleanRel)
	version, events := a.collab.eventsSince(path, since)
	writeJSON(w, http.StatusOK, map[string]any{
		"path":    path,
		"version": version,
		"events":  events,
	})
}

func writeSSE(w http.ResponseWriter, event collabEvent) {
	data, _ := json.Marshal(event)
	payloadSize := len(event.Type) + len(data) + len("event: \ndata: \n\n")
	_, _ = fmt.Fprintf(w, "event: %s\n", event.Type)
	_, _ = fmt.Fprintf(w, "data: %s\n", data)
	if payloadSize < minSSEFlushBytes {
		_, _ = fmt.Fprintf(w, ": %s\n", strings.Repeat(" ", minSSEFlushBytes-payloadSize))
	}
	_, _ = fmt.Fprint(w, "\n")
}

func publicCollabUser(user authUser) collabUser {
	return collabUser{ID: user.ID, Name: user.Name, Email: user.Email}
}
