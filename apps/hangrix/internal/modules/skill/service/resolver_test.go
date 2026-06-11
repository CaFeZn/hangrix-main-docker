package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	agentsessiondomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
	skilldomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/skill/domain"
)

// ---------------------------------------------------------------------------
// in-memory HostBlobReader stub
// ---------------------------------------------------------------------------

type memBlobs struct {
	files   map[string]string // path -> content
	dirs    map[string][]string // dir -> list of child paths
}

func (m *memBlobs) ReadBlob(_ context.Context, _, _, path string) ([]byte, bool) {
	v, ok := m.files[path]
	if !ok {
		return nil, false
	}
	return []byte(v), true
}

func (m *memBlobs) ListBlobs(_ context.Context, _, _, dir string) ([]string, bool) {
	v, ok := m.dirs[dir]
	if !ok {
		return nil, false
	}
	return v, true
}

var _ agentsessiondomain.HostBlobReader = (*memBlobs)(nil)

func newMemBlobs() *memBlobs {
	return &memBlobs{
		files: make(map[string]string),
		dirs:  make(map[string][]string),
	}
}

func (m *memBlobs) addSkill(slug, name, description, body string) {
	skillDir := ".hangrix/skills/" + slug
	// Populate the directory listing for .hangrix/skills so ListBlobs works.
	m.dirs[".hangrix/skills"] = append(m.dirs[".hangrix/skills"], slug)
	m.dirs[skillDir] = append(m.dirs[skillDir], "SKILL.md")
	fp := skillDir + "/SKILL.md"
	var fm strings.Builder
	fm.WriteString("---\n")
	if name != "" {
		fm.WriteString("name: " + name + "\n")
	} else {
		fm.WriteString("name:\n")
	}
	if description != "" {
		fm.WriteString("description: " + description + "\n")
	}
	fm.WriteString("---\n")
	m.files[fp] = fm.String() + body
}

func (m *memBlobs) addBadSkill(slug, content string) {
	skillDir := ".hangrix/skills/" + slug
	m.dirs[".hangrix/skills"] = append(m.dirs[".hangrix/skills"], slug)
	m.dirs[skillDir] = append(m.dirs[skillDir], "SKILL.md")
	m.files[skillDir+"/SKILL.md"] = content
}

// ---------------------------------------------------------------------------
// tests
// ---------------------------------------------------------------------------

func TestLoad_Valid(t *testing.T) {
	mb := newMemBlobs()
	mb.addSkill("foo", "Foo Skill", "Does things", "body text")
	r := NewResolver(&ResolverDeps{Blob: mb})
	sk, err := r.Load(context.Background(), "/tmp", "main", "foo")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if sk.Name != "Foo Skill" {
		t.Errorf("Name = %q, want %q", sk.Name, "Foo Skill")
	}
	if sk.Description != "Does things" {
		t.Errorf("Description = %q", sk.Description)
	}
	if sk.Body != "body text" {
		t.Errorf("Body = %q", sk.Body)
	}
	if sk.Slug != "foo" {
		t.Errorf("Slug = %q", sk.Slug)
	}
}

func TestLoad_NotFound(t *testing.T) {
	mb := newMemBlobs()
	r := NewResolver(&ResolverDeps{Blob: mb})
	_, err := r.Load(context.Background(), "/tmp", "main", "ghost")
	if !errors.Is(err, skilldomain.ErrSkillNotFound) {
		t.Fatalf("expected ErrSkillNotFound, got: %v", err)
	}
}

func TestLoad_InvalidSlug(t *testing.T) {
	mb := newMemBlobs()
	r := NewResolver(&ResolverDeps{Blob: mb})
	_, err := r.Load(context.Background(), "/tmp", "main", "../etc")
	if err == nil {
		t.Fatal("expected error for invalid slug")
	}
}

func TestLoad_InvalidFrontMatter(t *testing.T) {
	mb := newMemBlobs()
	mb.addBadSkill("bad", "no front matter here")
	r := NewResolver(&ResolverDeps{Blob: mb})
	_, err := r.Load(context.Background(), "/tmp", "main", "bad")
	if !errors.Is(err, skilldomain.ErrSkillInvalid) {
		t.Fatalf("expected ErrSkillInvalid, got: %v", err)
	}
}

func TestLoad_BadYAML(t *testing.T) {
	mb := newMemBlobs()
	mb.addBadSkill("bad", "---\nname: [unclosed\n---\nbody")
	r := NewResolver(&ResolverDeps{Blob: mb})
	_, err := r.Load(context.Background(), "/tmp", "main", "bad")
	if !errors.Is(err, skilldomain.ErrSkillInvalid) {
		t.Fatalf("expected ErrSkillInvalid, got: %v", err)
	}
}

func TestLoad_NameFallback(t *testing.T) {
	mb := newMemBlobs()
	mb.addSkill("foo", "", "", "body")
	r := NewResolver(&ResolverDeps{Blob: mb})
	sk, err := r.Load(context.Background(), "/tmp", "main", "foo")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if sk.Name != "foo" {
		t.Errorf("Name = %q, want %q", sk.Name, "foo")
	}
}

func TestList_Empty(t *testing.T) {
	mb := newMemBlobs()
	r := NewResolver(&ResolverDeps{Blob: mb})
	items, err := r.List(context.Background(), "/tmp", "main")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(items))
	}
}

func TestList_HappyPath(t *testing.T) {
	mb := newMemBlobs()
	mb.addSkill("foo", "Foo", "F", "b")
	mb.addSkill("bar", "Bar", "B", "b2")
	r := NewResolver(&ResolverDeps{Blob: mb})
	items, err := r.List(context.Background(), "/tmp", "main")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].Slug != "bar" {
		t.Errorf("first slug = %q, want bar", items[0].Slug)
	}
	if items[1].Slug != "foo" {
		t.Errorf("second slug = %q, want foo", items[1].Slug)
	}
}

func TestList_SkipsBadSkill(t *testing.T) {
	mb := newMemBlobs()
	mb.addSkill("good", "G", "", "b")
	mb.addBadSkill("bad", "garbage")
	r := NewResolver(&ResolverDeps{Blob: mb})
	items, err := r.List(context.Background(), "/tmp", "main")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item (good), got %d", len(items))
	}
	if items[0].Slug != "good" {
		t.Errorf("slug = %q, want good", items[0].Slug)
	}
}

// TestList_RealGit exercises the resolver against a real bare repo built with
// git, proving the git cat-file path works end-to-end.
func TestList_RealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	bare := t.TempDir()
	work := t.TempDir()

	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_TERMINAL_PROMPT=0",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	git("", "init", "-q", "--bare", "-b", "main", bare)
	git("", "init", "-q", "-b", "main", work)

	// Create .hangrix/skills/hello/SKILL.md
	skillDir := filepath.Join(work, ".hangrix", "skills", "hello")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: Hello Skill\ndescription: A test skill\n---\n\nThis is the body.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	git(work, "add", ".")
	git(work, "commit", "-q", "-m", "add skill")
	git(work, "push", "-q", bare, "main:refs/heads/main")

	imported := &gitBlobReader{}
	r := NewResolver(&ResolverDeps{Blob: imported})
	items, err := r.List(context.Background(), bare, "refs/heads/main")
	if err != nil {
		t.Fatalf("List via real git: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(items))
	}
	if items[0].Slug != "hello" || items[0].Name != "Hello Skill" {
		t.Errorf("unexpected skill: %+v", items[0])
	}
}

// gitBlobReader is a copy of agent_session/service.GitBlobReader imported
// inline — we cannot import that package without a cycle in tests.
type gitBlobReader struct{}

func (r *gitBlobReader) ReadBlob(ctx context.Context, repoFsPath, ref, path string) ([]byte, bool) {
	cmd := exec.CommandContext(ctx,
		"git",
		"--git-dir="+repoFsPath,
		"cat-file",
		"-p",
		ref+":"+path,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	return out, true
}

func (r *gitBlobReader) ListBlobs(ctx context.Context, repoFsPath, ref, dir string) ([]string, bool) {
	cmd := exec.CommandContext(ctx,
		"git",
		"--git-dir="+repoFsPath,
		"ls-tree",
		"--name-only",
		ref,
		strings.TrimSuffix(dir, "/")+"/",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	var paths []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			paths = append(paths, line)
		}
	}
	if len(paths) == 0 {
		return nil, false
	}
	return paths, true
}
