package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Saves are recorded as real git commits in a hidden bare repository under
// <root>/.branch/history.git. Each file gets its own commit chain:
//   refs/cur/<key>        the version the file on disk currently matches
//   refs/tips/<key>/<sha> every leaf of the version tree
// Restoring moves refs/cur to an older commit without rewriting anything, so
// the next save branches off that commit and the history forms a tree.
const historyDirName = ".branch"
const coalesceWindow = 120 * time.Second

type historyStore struct {
	mu      sync.Mutex
	gitDir  string
	enabled bool
	ready   bool
	gcOnce  sync.Once
}

type historyNode struct {
	ID        string `json:"id"`
	Parent    string `json:"parent,omitempty"`
	Time      string `json:"time"`
	Author    string `json:"author"`
	Current   bool   `json:"current"`
	Name      string `json:"name,omitempty"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

func newHistoryStore(root string, enabled bool) *historyStore {
	if enabled {
		if _, err := exec.LookPath("git"); err != nil {
			fmt.Println("Warning: git not found, save history is disabled")
			enabled = false
		}
	}
	return &historyStore{
		gitDir:  filepath.Join(root, historyDirName, "history.git"),
		enabled: enabled,
	}
}

func (s *historyStore) ensureRepoLocked() error {
	if !s.enabled {
		return errors.New("save history is disabled")
	}
	if s.ready {
		return nil
	}
	if _, err := os.Stat(filepath.Join(s.gitDir, "HEAD")); err != nil {
		if err := os.MkdirAll(s.gitDir, 0755); err != nil {
			return err
		}
		if _, err := s.git(nil, nil, "init", "--bare", "--quiet"); err != nil {
			return err
		}
	}
	s.ready = true
	// Coalesced saves leave unreachable commits behind; let git clean them
	// up occasionally so the history repo does not grow forever.
	s.gcOnce.Do(func() {
		go func() { _, _ = s.git(nil, nil, "gc", "--auto", "--quiet") }()
	})
	return nil
}

func (s *historyStore) git(stdin []byte, env []string, args ...string) (string, error) {
	out, err := s.gitRaw(stdin, env, args...)
	return strings.TrimSpace(out), err
}

// gitRaw preserves the exact output; file content must keep its trailing newline.
func (s *historyStore) gitRaw(stdin []byte, env []string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_DIR="+s.gitDir)
	cmd.Env = append(cmd.Env, env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], detail)
	}
	return out.String(), nil
}

func historyKey(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])
}

func gitIdentity(user authUser) []string {
	name := user.Name
	if name == "" {
		name = "Local user"
	}
	email := user.Email
	if email == "" {
		email = "local@branch"
	}
	return []string{
		"GIT_AUTHOR_NAME=" + name,
		"GIT_AUTHOR_EMAIL=" + email,
		"GIT_COMMITTER_NAME=" + name,
		"GIT_COMMITTER_EMAIL=" + email,
	}
}

// recordSave commits content as the new current version of path. Rapid saves
// from the same client collapse into one node instead of flooding the tree.
func (s *historyStore) recordSave(path string, content string, user authUser, clientID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureRepoLocked(); err != nil {
		return "", err
	}
	key := historyKey(path)

	blob, err := s.git([]byte(content), nil, "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	tree, err := s.git([]byte("100644 blob "+blob+"\tcontent.md\n"), nil, "mktree")
	if err != nil {
		return "", err
	}

	parent, _ := s.git(nil, nil, "rev-parse", "--verify", "--quiet", "refs/cur/"+key)
	replaced := ""
	if parent != "" {
		parentTree, err := s.git(nil, nil, "rev-parse", parent+"^{tree}")
		if err == nil && parentTree == tree {
			return parent, nil // content unchanged, no new node
		}
		if grand, ok := s.coalesceTarget(key, parent, clientID); ok {
			replaced = parent
			parent = grand
		}
	}

	args := []string{"commit-tree", tree, "-m", "save\n\nBranch-Client: " + clientID}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	commit, err := s.git(nil, gitIdentity(user), args...)
	if err != nil {
		return "", err
	}

	if _, err := s.git(nil, nil, "update-ref", "refs/tips/"+key+"/"+commit, commit); err != nil {
		return "", err
	}
	for _, gone := range []string{replaced, parent} {
		if gone != "" && gone != commit {
			_, _ = s.git(nil, nil, "update-ref", "-d", "refs/tips/"+key+"/"+gone)
		}
	}
	if _, err := s.git(nil, nil, "update-ref", "refs/cur/"+key, commit); err != nil {
		return "", err
	}
	return commit, nil
}

// coalesceTarget reports whether cur is a fresh leaf save by the same client
// that the new commit should replace, returning cur's parent if so.
func (s *historyStore) coalesceTarget(key string, cur string, clientID string) (string, bool) {
	if clientID == "" {
		return "", false
	}
	if _, err := s.git(nil, nil, "rev-parse", "--verify", "--quiet", "refs/tips/"+key+"/"+cur); err != nil {
		return "", false
	}
	meta, err := s.git(nil, nil, "log", "-1", "--format=%at%x00%P%x00%B", cur)
	if err != nil {
		return "", false
	}
	parts := strings.SplitN(meta, "\x00", 3)
	if len(parts) != 3 {
		return "", false
	}
	at, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Since(time.Unix(at, 0)) > coalesceWindow {
		return "", false
	}
	if !strings.Contains(parts[2], "Branch-Client: "+clientID) {
		return "", false
	}
	return firstField(parts[1]), true
}

// list returns every save of path as a flat node list; the tree shape is in
// the parent links.
func (s *historyStore) list(path string) ([]historyNode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureRepoLocked(); err != nil {
		return nil, err
	}
	key := historyKey(path)
	tipsRaw, err := s.git(nil, nil, "for-each-ref", "--format=%(objectname)", "refs/tips/"+key)
	if err != nil || tipsRaw == "" {
		return []historyNode{}, nil
	}
	tips := strings.Fields(tipsRaw)
	cur, _ := s.git(nil, nil, "rev-parse", "--verify", "--quiet", "refs/cur/"+key)

	args := append([]string{
		"log", "--date-order", "--numstat", "--notes=labels",
		"--format=%x01%H%x00%P%x00%aI%x00%an%x00%N%x02",
	}, tips...)
	out, err := s.git(nil, nil, args...)
	if err != nil {
		return nil, err
	}
	nodes := []historyNode{}
	for _, record := range strings.Split(out, "\x01") {
		head, rest, found := strings.Cut(record, "\x02")
		if !found {
			continue
		}
		parts := strings.SplitN(head, "\x00", 5)
		if len(parts) != 5 {
			continue
		}
		node := historyNode{
			ID:      parts[0],
			Parent:  firstField(parts[1]),
			Time:    parts[2],
			Author:  parts[3],
			Name:    strings.TrimSpace(parts[4]),
			Current: parts[0] == cur,
		}
		// --numstat lines after the format: "<added>\t<deleted>\t<file>"
		for _, line := range strings.Split(rest, "\n") {
			fields := strings.SplitN(line, "\t", 3)
			if len(fields) != 3 {
				continue
			}
			node.Additions, _ = strconv.Atoi(fields[0])
			node.Deletions, _ = strconv.Atoi(fields[1])
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

// The sha of git's well-known empty tree, used to diff root commits.
const emptyTreeID = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// diff returns the unified diff a save introduced relative to its parent.
func (s *historyStore) diff(path string, id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureRepoLocked(); err != nil {
		return "", err
	}
	if !commitIDPattern.MatchString(id) {
		return "", errors.New("invalid version id")
	}
	if !s.belongsToFileLocked(path, id) {
		return "", errors.New("version not found for this file")
	}
	base, err := s.git(nil, nil, "rev-parse", "--verify", "--quiet", id+"^")
	if err != nil {
		base = emptyTreeID
	}
	return s.gitRaw(nil, nil, "diff", "--no-color", base, id, "--", "content.md")
}

// setLabel names a version (empty name removes it). Stored as a git note so
// the commit id stays stable.
func (s *historyStore) setLabel(path string, id string, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureRepoLocked(); err != nil {
		return err
	}
	if !commitIDPattern.MatchString(id) {
		return errors.New("invalid version id")
	}
	if !s.belongsToFileLocked(path, id) {
		return errors.New("version not found for this file")
	}
	name = strings.Join(strings.Fields(name), " ")
	if len(name) > 120 {
		name = name[:120]
	}
	if name == "" {
		_, _ = s.git(nil, nil, "notes", "--ref=labels", "remove", "--ignore-missing", id)
		return nil
	}
	_, err := s.git(nil, nil, "notes", "--ref=labels", "add", "-f", "-m", name, id)
	return err
}

// rename moves a file's entire version tree to a new path key.
func (s *historyStore) rename(oldPath string, newPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureRepoLocked(); err != nil {
		return err
	}
	oldKey := historyKey(oldPath)
	newKey := historyKey(newPath)
	out, err := s.git(nil, nil, "for-each-ref", "--format=%(refname) %(objectname)", "refs/tips/"+oldKey, "refs/cur/"+oldKey)
	if err != nil || out == "" {
		return err
	}
	for _, line := range strings.Split(out, "\n") {
		ref, sha, found := strings.Cut(line, " ")
		if !found {
			continue
		}
		newRef := strings.Replace(ref, oldKey, newKey, 1)
		if _, err := s.git(nil, nil, "update-ref", newRef, sha); err != nil {
			return err
		}
		if _, err := s.git(nil, nil, "update-ref", "-d", ref); err != nil {
			return err
		}
	}
	return nil
}

func firstField(value string) string {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

var commitIDPattern = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

// contentAt returns the saved content of path at a given history commit.
func (s *historyStore) contentAt(path string, id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.contentAtLocked(path, id)
}

func (s *historyStore) contentAtLocked(path string, id string) (string, error) {
	if err := s.ensureRepoLocked(); err != nil {
		return "", err
	}
	if !commitIDPattern.MatchString(id) {
		return "", errors.New("invalid version id")
	}
	if !s.belongsToFileLocked(path, id) {
		return "", errors.New("version not found for this file")
	}
	return s.gitRaw(nil, nil, "cat-file", "blob", id+":content.md")
}

// restore points refs/cur at an older commit and returns its content. The
// caller writes the file; the next save branches off the restored commit.
func (s *historyStore) restore(path string, id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	content, err := s.contentAtLocked(path, id)
	if err != nil {
		return "", err
	}
	if _, err := s.git(nil, nil, "update-ref", "refs/cur/"+historyKey(path), id); err != nil {
		return "", err
	}
	return content, nil
}

func (s *historyStore) belongsToFileLocked(path string, id string) bool {
	key := historyKey(path)
	tipsRaw, err := s.git(nil, nil, "for-each-ref", "--format=%(objectname)", "refs/tips/"+key)
	if err != nil || tipsRaw == "" {
		return false
	}
	for _, tip := range strings.Fields(tipsRaw) {
		if tip == id {
			return true
		}
		if _, err := s.git(nil, nil, "merge-base", "--is-ancestor", id, tip); err == nil {
			return true
		}
	}
	return false
}
