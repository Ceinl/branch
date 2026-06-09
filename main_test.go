package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testApp(t *testing.T) *app {
	t.Helper()
	root := t.TempDir()
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return &app{root: root, rootReal: rootReal, sessions: newSessionStore(""), shoo: newShooVerifier(), collab: newCollabHub(), history: newHistoryStore(root, false)}
}

func TestReadOnlyRejectsMutations(t *testing.T) {
	a := testApp(t)
	a.readOnly = true
	if err := os.WriteFile(filepath.Join(a.root, "doc.md"), []byte("# hi\n"), 0644); err != nil {
		t.Fatal(err)
	}

	requests := []*http.Request{
		httptest.NewRequest(http.MethodPut, "/api/file", strings.NewReader(`{"path":"doc.md","content":"x"}`)),
		httptest.NewRequest(http.MethodPost, "/api/file", strings.NewReader(`{"path":"new.md","kind":"file"}`)),
		httptest.NewRequest(http.MethodDelete, "/api/file?path=doc.md", nil),
		httptest.NewRequest(http.MethodPost, "/api/file/rename", strings.NewReader(`{"path":"doc.md","to":"other.md"}`)),
		httptest.NewRequest(http.MethodPost, "/api/file/restore", strings.NewReader(`{"path":"doc.md","id":"abc"}`)),
		httptest.NewRequest(http.MethodPost, "/api/file/history/label", strings.NewReader(`{"path":"doc.md","id":"abc","name":"x"}`)),
	}
	for _, r := range requests {
		w := httptest.NewRecorder()
		a.handleFile(w, r)
		switch r.URL.Path {
		case "/api/file/rename":
			w = httptest.NewRecorder()
			a.handleFileRename(w, r)
		case "/api/file/restore":
			w = httptest.NewRecorder()
			a.handleFileRestore(w, r)
		case "/api/file/history/label":
			w = httptest.NewRecorder()
			a.handleFileHistoryLabel(w, r)
		}
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s: got %d, want 403", r.Method, r.URL.Path, w.Code)
		}
	}

	// Reads still work.
	w := httptest.NewRecorder()
	a.handleFile(w, httptest.NewRequest(http.MethodGet, "/api/file?path=doc.md", nil))
	if w.Code != http.StatusOK {
		t.Errorf("read in read-only mode: got %d, want 200", w.Code)
	}
}

func TestResolveRejectsPathEscape(t *testing.T) {
	a := testApp(t)
	outside := filepath.Join(filepath.Dir(a.root), "outside.md")
	if err := os.WriteFile(outside, []byte("outside"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	if _, _, err := a.resolveExisting("../outside.md"); err == nil {
		t.Fatal("expected path escape to be rejected")
	}
}

func TestResolveRejectsEscapingSymlink(t *testing.T) {
	a := testApp(t)
	outside := filepath.Join(filepath.Dir(a.root), "outside.md")
	if err := os.WriteFile(outside, []byte("outside"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	link := filepath.Join(a.root, "link.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	if _, _, err := a.resolveExisting("link.md"); err == nil {
		t.Fatal("expected escaping symlink to be rejected")
	}
}

func TestAtomicWriteCreatesFile(t *testing.T) {
	a := testApp(t)
	path, _, err := a.resolveWritable("notes/doc.md")
	if err == nil {
		t.Fatal("expected missing parent to fail before creating directories")
	}

	if err := os.Mkdir(filepath.Join(a.root, "notes"), 0755); err != nil {
		t.Fatal(err)
	}
	path, _, err = a.resolveWritable("notes/doc.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(path, []byte("# Hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "# Hello\n" {
		t.Fatalf("unexpected content: %q", data)
	}
}

func TestCollabStreamsAnnounceCollaborators(t *testing.T) {
	a := testApp(t)
	if err := os.WriteFile(filepath.Join(a.root, "note.md"), []byte("# Note\n"), 0644); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.withAuth(a.handleFileStream)(w, r)
	}))
	t.Cleanup(server.Close)

	aliceResp := openCollabStream(t, server.URL, "alice")
	defer aliceResp.Body.Close()
	if got := aliceResp.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q, want no", got)
	}
	if got := aliceResp.Header.Get("Cache-Control"); !strings.Contains(got, "no-transform") {
		t.Fatalf("Cache-Control = %q, want no-transform", got)
	}
	aliceEvents := sseEvents(t, aliceResp)
	if event := readSSEEvent(t, aliceEvents); event.Type != "snapshot" {
		t.Fatalf("alice first event = %q, want snapshot", event.Type)
	}

	bobResp := openCollabStream(t, server.URL, "bob")
	defer bobResp.Body.Close()
	bobEvents := sseEvents(t, bobResp)
	if event := readSSEEvent(t, bobEvents); event.Type != "snapshot" {
		t.Fatalf("bob first event = %q, want snapshot", event.Type)
	}

	alicePresence := readSSEEvent(t, aliceEvents)
	if alicePresence.Type != "presence" || alicePresence.ClientID != "bob" {
		t.Fatalf("alice presence event = %#v, want bob presence", alicePresence)
	}
	bobPresence := readSSEEvent(t, bobEvents)
	if bobPresence.Type != "presence" || bobPresence.ClientID != "alice" {
		t.Fatalf("bob presence event = %#v, want alice presence", bobPresence)
	}
}

func TestCollabDraftBroadcastsContent(t *testing.T) {
	a := testApp(t)
	if err := os.WriteFile(filepath.Join(a.root, "note.md"), []byte("# Note\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/file/stream", a.withAuth(a.handleFileStream))
	mux.HandleFunc("/api/file/collab", a.withAPI(a.handleFileCollab))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	aliceResp := openCollabStream(t, server.URL, "alice")
	defer aliceResp.Body.Close()
	aliceEvents := sseEvents(t, aliceResp)
	if event := readSSEEvent(t, aliceEvents); event.Type != "snapshot" {
		t.Fatalf("alice first event = %q, want snapshot", event.Type)
	}

	payload := []byte(`{"path":"note.md","clientId":"bob","content":"# Note\n\nBob was here\n","cursor":{"blockIndex":1,"offset":12}}`)
	resp, err := http.Post(server.URL+"/api/file/collab", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("collab post status = %s, want 200 OK", resp.Status)
	}

	draft := readSSEEvent(t, aliceEvents)
	if draft.Type != "draft" || draft.ClientID != "bob" || draft.Content == nil || *draft.Content != "# Note\n\nBob was here\n" {
		t.Fatalf("draft event = %#v, want bob content draft", draft)
	}
	if draft.Cursor == nil || draft.Cursor.BlockIndex != 1 || draft.Cursor.Offset != 12 {
		t.Fatalf("draft cursor = %#v, want block 1 offset 12", draft.Cursor)
	}
}

func TestCollabPollReturnsRecentEvents(t *testing.T) {
	a := testApp(t)
	if err := os.WriteFile(filepath.Join(a.root, "note.md"), []byte("# Note\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/file/collab", a.withAPI(a.handleFileCollab))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	payload := []byte(`{"path":"note.md","clientId":"bob","content":"# Note\n\nBob was here\n"}`)
	resp, err := http.Post(server.URL+"/api/file/collab", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("collab post status = %s, want 200 OK", resp.Status)
	}

	pollResp, err := http.Get(server.URL + "/api/file/collab?path=note.md&since=0&clientId=alice")
	if err != nil {
		t.Fatal(err)
	}
	defer pollResp.Body.Close()
	if pollResp.StatusCode != http.StatusOK {
		t.Fatalf("collab poll status = %s, want 200 OK", pollResp.Status)
	}
	var body struct {
		Version uint64        `json:"version"`
		Events  []collabEvent `json:"events"`
	}
	if err := json.NewDecoder(pollResp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Version == 0 {
		t.Fatal("poll version = 0, want non-zero")
	}
	if len(body.Events) != 1 {
		t.Fatalf("poll returned %d events, want 1", len(body.Events))
	}
	event := body.Events[0]
	if event.Type != "draft" || event.ClientID != "bob" || event.Content == nil || *event.Content != "# Note\n\nBob was here\n" {
		t.Fatalf("poll event = %#v, want bob draft", event)
	}
}

func openCollabStream(t *testing.T, baseURL string, clientID string) *http.Response {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/file/stream?path=note.md&clientId="+clientID, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		t.Fatalf("stream status = %s, want 200 OK", resp.Status)
	}
	return resp
}

func sseEvents(t *testing.T, resp *http.Response) <-chan collabEvent {
	t.Helper()
	events := make(chan collabEvent)
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(resp.Body)
		var eventType string
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				eventType = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				var event collabEvent
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
					return
				}
				if event.Type == "" {
					event.Type = eventType
				}
				events <- event
			}
		}
	}()
	return events
}

func readSSEEvent(t *testing.T, events <-chan collabEvent) collabEvent {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("stream closed before next event")
		}
		return event
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for SSE event")
	}
	return collabEvent{}
}
