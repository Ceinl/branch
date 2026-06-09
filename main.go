package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

//go:embed web
var webFiles embed.FS

const maxEditableBytes = 12 * 1024 * 1024

type app struct {
	root      string
	rootReal  string
	appOrigin string
	auth      bool
	sessions  *sessionStore
	shoo      *shooVerifier
	collab    *collabHub
	history   *historyStore
	allowed   map[string]bool
	readOnly  bool
}

// denyReadOnly rejects the request when the server is read-only, so every
// mutating handler stays guarded even if a client calls the API directly.
func (a *app) denyReadOnly(w http.ResponseWriter) bool {
	if a.readOnly {
		writeError(w, http.StatusForbidden, "server is read-only")
	}
	return a.readOnly
}

// emailAllowed reports whether a verified user may use this server. An empty
// allowlist admits every authenticated user.
func (a *app) emailAllowed(email string) bool {
	if len(a.allowed) == 0 {
		return true
	}
	return a.allowed[strings.ToLower(strings.TrimSpace(email))]
}

func parseAllowList(value string) map[string]bool {
	allowed := map[string]bool{}
	for _, email := range strings.Split(value, ",") {
		email = strings.ToLower(strings.TrimSpace(email))
		if email != "" {
			allowed[email] = true
		}
	}
	return allowed
}

type fileEntry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	Size      int64  `json:"size"`
	Modified  string `json:"modified"`
	Markdown  bool   `json:"markdown"`
	Editable  bool   `json:"editable"`
	Extension string `json:"extension"`
	IsSymlink bool   `json:"isSymlink"`
}

func main() {
	cfg := parseCLI()

	root, err := filepath.Abs(cfg.root)
	if err != nil {
		log.Fatalf("resolve root: %v", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		log.Fatalf("open root: %v", err)
	}
	if !info.IsDir() {
		log.Fatalf("root must be a directory: %s", root)
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		log.Fatalf("resolve root symlink: %v", err)
	}
	a := &app{
		root:      root,
		rootReal:  rootReal,
		appOrigin: cfg.origin,
		auth:      cfg.origin != "",
		sessions:  newSessionStore(),
		shoo:      newShooVerifier(),
		collab:    newCollabHub(),
		history:   newHistoryStore(root, !cfg.noHistory),
		allowed:   parseAllowList(cfg.allow),
		readOnly:  cfg.readOnly,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", a.handleIndex)
	mux.HandleFunc("/shoo/callback", a.handleIndex)
	mux.HandleFunc("/app.js", a.handleAsset("web/app.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("/styles.css", a.handleAsset("web/styles.css", "text/css; charset=utf-8"))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/api/config", a.handleConfig)
	mux.HandleFunc("/api/session", a.handleSession)
	mux.HandleFunc("/api/root", a.withAPI(a.handleRoot))
	mux.HandleFunc("/api/files", a.withAPI(a.handleFiles))
	mux.HandleFunc("/api/file", a.withAPI(a.handleFile))
	mux.HandleFunc("/api/file/collab", a.withAPI(a.handleFileCollab))
	mux.HandleFunc("/api/file/stream", a.withAuth(a.handleFileStream))
	mux.HandleFunc("/api/file/history", a.withAPI(a.handleFileHistory))
	mux.HandleFunc("/api/file/history/content", a.withAPI(a.handleFileHistoryContent))
	mux.HandleFunc("/api/file/history/diff", a.withAPI(a.handleFileHistoryDiff))
	mux.HandleFunc("/api/file/history/label", a.withAPI(a.handleFileHistoryLabel))
	mux.HandleFunc("/api/file/restore", a.withAPI(a.handleFileRestore))
	mux.HandleFunc("/api/file/rename", a.withAPI(a.handleFileRename))

	listener, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		log.Fatalf("listen on %s: %v", cfg.addr, err)
	}
	openURL := fmt.Sprintf("http://%s/", listenerHost(listener))
	if cfg.origin != "" {
		openURL = cfg.origin
	}
	fmt.Printf("Branch %s serving %s\n", appVersion, root)
	fmt.Printf("Open %s\n", openURL)
	if cfg.share {
		fmt.Printf("Listening on %s for shared access\n", listener.Addr())
	}
	if a.readOnly {
		fmt.Println("Read-only mode: editing is disabled")
	}
	if a.history.enabled && !a.readOnly {
		fmt.Printf("Save history in %s\n", filepath.Join(historyDirName, "history.git"))
	}
	if cfg.open {
		openBrowser(openURL)
	}
	server := &http.Server{Handler: mux}
	go func() {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		<-ctx.Done()
		fmt.Println("\nShutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// listenerHost rewrites wildcard listen addresses into something a browser
// can actually open, keeping the real (possibly auto-assigned) port.
func listenerHost(listener net.Listener) string {
	addr := listener.Addr().String()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "::" || host == "0.0.0.0" || host == "" {
		host = "localhost"
	}
	return net.JoinHostPort(host, port)
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		fmt.Printf("Could not open browser: %v\n", err)
	}
}

// Overridden at release time via -ldflags "-X main.appVersion=v0.x.y".
var appVersion = "dev"

type cliConfig struct {
	addr      string
	origin    string
	root      string
	share     bool
	open      bool
	noHistory bool
	allow     string
	readOnly  bool
}

func parseCLI() cliConfig {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "share":
			return parseShareCLI(os.Args[2:])
		case "version", "--version", "-v":
			fmt.Printf("branch %s\n", appVersion)
			os.Exit(0)
		case "help", "--help", "-h":
			parseServeCLI([]string{"--help"})
		}
	}
	return parseServeCLI(os.Args[1:])
}

func parseServeCLI(args []string) cliConfig {
	fs := flag.NewFlagSet("branch", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8080", "address to listen on (use :0 for a random port)")
	origin := fs.String("origin", "", "public origin for Shoo auth, e.g. https://docs.example.com")
	open := fs.Bool("open", false, "open the browser after starting")
	noHistory := fs.Bool("no-history", false, "disable git-based save history")
	allow := fs.String("allow", "", "comma-separated emails allowed to sign in (shared mode)")
	readOnly := fs.Bool("read-only", false, "serve files for reading only, editing disabled")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Branch %s - self-hosted Markdown editor with git-based save history\n\n", appVersion)
		fmt.Fprintf(fs.Output(), "Usage:\n")
		fmt.Fprintf(fs.Output(), "  branch [flags] [path]              serve a folder locally\n")
		fmt.Fprintf(fs.Output(), "  branch share https://url [path]    serve with Shoo sign-in for collaborators\n")
		fmt.Fprintf(fs.Output(), "  branch version                     print the version\n\n")
		fmt.Fprintf(fs.Output(), "Examples:\n")
		fmt.Fprintf(fs.Output(), "  branch .\n")
		fmt.Fprintf(fs.Output(), "  branch --open --addr :0 ~/notes\n")
		fmt.Fprintf(fs.Output(), "  branch share https://docs.example.com .\n\n")
		fmt.Fprintf(fs.Output(), "Flags:\n")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	rootArg := "."
	if fs.NArg() > 0 {
		rootArg = fs.Arg(0)
	}
	cleanOrigin := strings.TrimRight(*origin, "/")
	if cleanOrigin != "" {
		validatePublicOrigin(cleanOrigin)
	}
	return cliConfig{addr: *addr, origin: cleanOrigin, root: rootArg, open: *open, noHistory: *noHistory, allow: *allow, readOnly: *readOnly}
}

func parseShareCLI(args []string) cliConfig {
	fs := flag.NewFlagSet("branch share", flag.ExitOnError)
	addr := fs.String("addr", "0.0.0.0:8080", "address to listen on")
	open := fs.Bool("open", false, "open the browser after starting")
	noHistory := fs.Bool("no-history", false, "disable git-based save history")
	allow := fs.String("allow", "", "comma-separated emails allowed to sign in")
	readOnly := fs.Bool("read-only", false, "serve files for reading only, editing disabled")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: branch share [--addr host:port] https://public-url [path]\n")
		fmt.Fprintf(fs.Output(), "Example: branch share https://docs.example.com .\n\n")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(2)
	}
	origin := strings.TrimRight(fs.Arg(0), "/")
	validatePublicOrigin(origin)
	rootArg := "."
	if fs.NArg() > 1 {
		rootArg = fs.Arg(1)
	}
	return cliConfig{addr: *addr, origin: origin, root: rootArg, share: true, open: *open, noHistory: *noHistory, allow: *allow, readOnly: *readOnly}
}

func validatePublicOrigin(origin string) {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		log.Fatalf("origin must be a bare HTTPS origin, e.g. https://docs.example.com")
	}
}

func randomToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (a *app) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/shoo/callback" {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(webFiles, "web/index.html")
	if err != nil {
		http.Error(w, "index not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func (a *app) handleAsset(path string, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := fs.ReadFile(webFiles, path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if contentType == "" {
			contentType = mime.TypeByExtension(filepath.Ext(path))
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(data)
	}
}

func (a *app) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	origin := a.shooOriginForRequest(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"origin":        origin,
		"redirectURI":   strings.TrimRight(origin, "/") + "/shoo/callback",
		"hasOriginFlag": a.appOrigin != "",
		"authRequired":  a.auth,
		"readOnly":      a.readOnly,
	})
}

func (a *app) withAPI(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		user, err := a.requireUser(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		next(w, r.WithContext(withUser(r.Context(), user)))
	}
}

func (a *app) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := a.requireUser(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		next(w, r.WithContext(withUser(r.Context(), user)))
	}
}

func (a *app) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"name": filepath.Base(a.root),
		"path": a.root,
	})
}

func (a *app) handleFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
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
	if !info.IsDir() {
		writeError(w, http.StatusBadRequest, "path is not a directory")
		return
	}

	entries, err := os.ReadDir(full)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]fileEntry, 0, len(entries))
	for _, entry := range entries {
		entryInfo, err := entry.Info()
		if err != nil {
			continue
		}
		name := entry.Name()
		if cleanRel == "" && name == historyDirName {
			continue
		}
		childRel := joinRel(cleanRel, name)
		kind := "file"
		if entry.IsDir() {
			kind = "directory"
		}
		ext := strings.ToLower(filepath.Ext(name))
		items = append(items, fileEntry{
			Name:      name,
			Path:      slashPath(childRel),
			Kind:      kind,
			Size:      entryInfo.Size(),
			Modified:  entryInfo.ModTime().Format(time.RFC3339),
			Markdown:  isMarkdown(name),
			Editable:  kind == "file" && isLikelyText(name),
			Extension: strings.TrimPrefix(ext, "."),
			IsSymlink: entryInfo.Mode()&os.ModeSymlink != 0,
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind == "directory"
		}
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"path":  slashPath(cleanRel),
		"items": items,
	})
}

func (a *app) handleFile(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.handleReadFile(w, r)
	case http.MethodPut:
		a.handleSaveFile(w, r)
	case http.MethodPost:
		a.handleCreate(w, r)
	case http.MethodDelete:
		a.handleDeleteFile(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *app) handleReadFile(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, http.StatusRequestEntityTooLarge, "file is too large for the editor")
		return
	}
	data, err := os.ReadFile(full)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !utf8.Valid(data) {
		writeError(w, http.StatusUnsupportedMediaType, "file is not valid UTF-8 text")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":     slashPath(cleanRel),
		"name":     filepath.Base(cleanRel),
		"content":  string(data),
		"size":     info.Size(),
		"modified": info.ModTime().Format(time.RFC3339),
		"markdown": isMarkdown(cleanRel),
	})
}

func (a *app) handleSaveFile(w http.ResponseWriter, r *http.Request) {
	if a.denyReadOnly(w) {
		return
	}
	var req struct {
		Path         string `json:"path"`
		Content      string `json:"content"`
		ClientID     string `json:"clientId"`
		BaseModified string `json:"baseModified"`
		Force        bool   `json:"force"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(req.Content) > maxEditableBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "content is too large for the editor")
		return
	}
	full, cleanRel, err := a.resolveWritable(req.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	mode := fs.FileMode(0644)
	if info, err := os.Stat(full); err == nil {
		if info.IsDir() {
			writeError(w, http.StatusBadRequest, "path is a directory")
			return
		}
		mode = info.Mode().Perm()
		// Optimistic concurrency: reject the save if the file changed since
		// the client last loaded it, unless the client insists.
		current := info.ModTime().Format(time.RFC3339)
		if !req.Force && req.BaseModified != "" && req.BaseModified != current {
			writeError(w, http.StatusConflict, "file changed since it was loaded")
			return
		}
	}
	if err := atomicWrite(full, []byte(req.Content), mode); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	info, err := os.Stat(full)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	versionID := ""
	if a.history.enabled && isMarkdown(cleanRel) {
		versionID, err = a.history.recordSave(slashPath(cleanRel), req.Content, userFromRequest(r), req.ClientID)
		if err != nil {
			log.Printf("history: record save of %s: %v", slashPath(cleanRel), err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":     slashPath(cleanRel),
		"size":     info.Size(),
		"modified": info.ModTime().Format(time.RFC3339),
		"version":  versionID,
	})
	a.collab.broadcastUpdate(slashPath(cleanRel), req.Content, info.ModTime().Format(time.RFC3339), userFromRequest(r), req.ClientID)
}

func (a *app) handleCreate(w http.ResponseWriter, r *http.Request) {
	if a.denyReadOnly(w) {
		return
	}
	var req struct {
		Path    string `json:"path"`
		Kind    string `json:"kind"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(req.Content) > maxEditableBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "content is too large for the editor")
		return
	}
	if req.Kind == "" {
		req.Kind = "file"
	}
	full, cleanRel, err := a.resolveWritable(req.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch req.Kind {
	case "directory":
		if err := os.Mkdir(full, 0755); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	case "file":
		file, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		_, writeErr := file.WriteString(req.Content)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			_ = os.Remove(full)
			if writeErr != nil {
				writeError(w, http.StatusInternalServerError, writeErr.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, closeErr.Error())
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "kind must be file or directory")
		return
	}
	if req.Kind == "file" && a.history.enabled && isMarkdown(cleanRel) {
		if _, err := a.history.recordSave(slashPath(cleanRel), req.Content, userFromRequest(r), ""); err != nil {
			log.Printf("history: record create of %s: %v", slashPath(cleanRel), err)
		}
	}
	writeJSON(w, http.StatusCreated, map[string]string{"path": slashPath(cleanRel)})
}

func (a *app) handleFileHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	_, cleanRel, err := a.resolveExisting(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !a.history.enabled {
		writeJSON(w, http.StatusOK, map[string]any{"path": slashPath(cleanRel), "enabled": false, "nodes": []historyNode{}})
		return
	}
	nodes, err := a.history.list(slashPath(cleanRel))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": slashPath(cleanRel), "enabled": true, "nodes": nodes})
}

func (a *app) handleFileHistoryContent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	_, cleanRel, err := a.resolveExisting(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	content, err := a.history.contentAt(slashPath(cleanRel), r.URL.Query().Get("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":    slashPath(cleanRel),
		"id":      r.URL.Query().Get("id"),
		"content": content,
	})
}

func (a *app) handleFileRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if a.denyReadOnly(w) {
		return
	}
	var req struct {
		Path     string `json:"path"`
		ID       string `json:"id"`
		ClientID string `json:"clientId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	full, cleanRel, err := a.resolveWritable(req.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	mode := fs.FileMode(0644)
	if info, err := os.Stat(full); err == nil {
		if info.IsDir() {
			writeError(w, http.StatusBadRequest, "path is a directory")
			return
		}
		mode = info.Mode().Perm()
	}
	content, err := a.history.restore(slashPath(cleanRel), req.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if err := atomicWrite(full, []byte(content), mode); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	info, err := os.Stat(full)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":     slashPath(cleanRel),
		"id":       req.ID,
		"content":  content,
		"modified": info.ModTime().Format(time.RFC3339),
	})
	a.collab.broadcastUpdate(slashPath(cleanRel), content, info.ModTime().Format(time.RFC3339), userFromRequest(r), req.ClientID)
}

func (a *app) handleFileHistoryDiff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	_, cleanRel, err := a.resolveExisting(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	diff, err := a.history.diff(slashPath(cleanRel), r.URL.Query().Get("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path": slashPath(cleanRel),
		"id":   r.URL.Query().Get("id"),
		"diff": diff,
	})
}

func (a *app) handleFileHistoryLabel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if a.denyReadOnly(w) {
		return
	}
	var req struct {
		Path string `json:"path"`
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	_, cleanRel, err := a.resolveExisting(req.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.history.setLabel(slashPath(cleanRel), req.ID, req.Name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": slashPath(cleanRel), "id": req.ID, "name": req.Name})
}

func (a *app) handleFileRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if a.denyReadOnly(w) {
		return
	}
	var req struct {
		Path string `json:"path"`
		To   string `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
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
		writeError(w, http.StatusBadRequest, "renaming folders is not supported yet")
		return
	}
	target, targetRel, err := a.resolveWritable(req.To)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := os.Lstat(target); err == nil {
		writeError(w, http.StatusConflict, "a file with that name already exists")
		return
	}
	if err := os.Rename(full, target); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if a.history.enabled {
		if err := a.history.rename(slashPath(cleanRel), slashPath(targetRel)); err != nil {
			log.Printf("history: rename %s to %s: %v", slashPath(cleanRel), slashPath(targetRel), err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": slashPath(targetRel)})
}

func (a *app) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	if a.denyReadOnly(w) {
		return
	}
	full, cleanRel, err := a.resolveExisting(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if cleanRel == "" {
		writeError(w, http.StatusBadRequest, "refusing to delete server root")
		return
	}
	info, err := os.Stat(full)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if info.IsDir() {
		entries, err := os.ReadDir(full)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if len(entries) > 0 {
			writeError(w, http.StatusBadRequest, "folder is not empty")
			return
		}
	}
	// History refs are kept on purpose: recreating the file at the same path
	// continues its old version tree.
	if err := os.Remove(full); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": slashPath(cleanRel)})
}

func (a *app) resolveExisting(rel string) (string, string, error) {
	full, cleanRel, err := a.resolveLexical(rel)
	if err != nil {
		return "", "", err
	}
	real, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", "", err
	}
	if !inside(a.rootReal, real) {
		return "", "", errors.New("path escapes server root")
	}
	return real, cleanRel, nil
}

func (a *app) resolveWritable(rel string) (string, string, error) {
	full, cleanRel, err := a.resolveLexical(rel)
	if err != nil {
		return "", "", err
	}
	if cleanRel == "" {
		return "", "", errors.New("refusing to write server root")
	}
	if info, err := os.Lstat(full); err == nil && info.Mode()&os.ModeSymlink != 0 {
		real, err := filepath.EvalSymlinks(full)
		if err != nil {
			return "", "", err
		}
		if !inside(a.rootReal, real) {
			return "", "", errors.New("path escapes server root")
		}
	}
	parent := filepath.Dir(full)
	parentReal, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", "", fmt.Errorf("resolve parent: %w", err)
	}
	if !inside(a.rootReal, parentReal) {
		return "", "", errors.New("path escapes server root")
	}
	return full, cleanRel, nil
}

func (a *app) resolveLexical(rel string) (string, string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "/" || rel == "." {
		rel = ""
	}
	rel = filepath.FromSlash(rel)
	if filepath.IsAbs(rel) {
		return "", "", errors.New("absolute paths are not allowed")
	}
	cleanRel := filepath.Clean(rel)
	if cleanRel == "." {
		cleanRel = ""
	}
	if cleanRel == historyDirName || strings.HasPrefix(cleanRel, historyDirName+string(filepath.Separator)) {
		return "", "", errors.New("path is reserved for Branch internals")
	}
	full := filepath.Join(a.root, cleanRel)
	fullAbs, err := filepath.Abs(full)
	if err != nil {
		return "", "", err
	}
	if !inside(a.root, fullAbs) {
		return "", "", errors.New("path escapes server root")
	}
	return fullAbs, cleanRel, nil
}

func inside(root string, full string) bool {
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return false
	}
	return rel == "." || (rel != "" && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func joinRel(base string, name string) string {
	if base == "" {
		return name
	}
	return filepath.Join(base, name)
}

func slashPath(p string) string {
	if p == "." {
		return ""
	}
	return filepath.ToSlash(p)
}

func isMarkdown(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".markdown", ".mdown", ".mkd":
		return true
	default:
		return false
	}
}

func isLikelyText(name string) bool {
	if isMarkdown(name) {
		return true
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".txt", ".text", ".json", ".yaml", ".yml", ".toml", ".csv", ".log", ".go", ".js", ".ts", ".tsx", ".jsx", ".html", ".css", ".scss", ".xml", ".sh", ".env", ".ini", ".conf":
		return true
	default:
		return !strings.Contains(filepath.Base(name), ".")
	}
}

func atomicWrite(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".branch-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
