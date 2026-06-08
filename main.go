package main

import (
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
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

//go:embed web
var webFiles embed.FS

const maxEditableBytes = 12 * 1024 * 1024

type app struct {
	root     string
	rootReal string
	token    string
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
	addr := flag.String("addr", "127.0.0.1:8080", "address to listen on")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: branch [--addr host:port] [path]\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Example: branch .\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	rootArg := "."
	if flag.NArg() > 0 {
		rootArg = flag.Arg(0)
	}

	root, err := filepath.Abs(rootArg)
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
	token, err := randomToken()
	if err != nil {
		log.Fatalf("create token: %v", err)
	}

	a := &app{root: root, rootReal: rootReal, token: token}
	mux := http.NewServeMux()
	mux.HandleFunc("/", a.handleIndex)
	mux.HandleFunc("/app.js", a.handleAsset("web/app.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("/styles.css", a.handleAsset("web/styles.css", "text/css; charset=utf-8"))
	mux.HandleFunc("/api/root", a.withAPI(a.handleRoot))
	mux.HandleFunc("/api/files", a.withAPI(a.handleFiles))
	mux.HandleFunc("/api/file", a.withAPI(a.handleFile))

	url := fmt.Sprintf("http://%s/?token=%s", *addr, token)
	fmt.Printf("Branch serving %s\n", root)
	fmt.Printf("Open %s\n", url)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
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
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if token := r.URL.Query().Get("token"); token != "" && token == a.token {
		http.SetCookie(w, &http.Cookie{
			Name:     "branch_token",
			Value:    token,
			Path:     "/",
			SameSite: http.SameSiteStrictMode,
			HttpOnly: false,
		})
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

func (a *app) withAPI(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		if !a.authorized(r) {
			writeError(w, http.StatusUnauthorized, "missing or invalid token")
			return
		}
		next(w, r)
	}
}

func (a *app) authorized(r *http.Request) bool {
	if r.Header.Get("X-Branch-Token") == a.token {
		return true
	}
	cookie, err := r.Cookie("branch_token")
	return err == nil && cookie.Value == a.token
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
	var req struct {
		Path    string `json:"path"`
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
	if err := atomicWrite(full, []byte(req.Content), mode); err != nil {
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
		"size":     info.Size(),
		"modified": info.ModTime().Format(time.RFC3339),
	})
}

func (a *app) handleCreate(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusCreated, map[string]string{"path": slashPath(cleanRel)})
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
