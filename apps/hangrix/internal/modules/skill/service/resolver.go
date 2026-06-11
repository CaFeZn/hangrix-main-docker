// Package service implements skill/domain.Resolver using the existing
// HostBlobReader (git cat-file wrapper) to read skill files from a bare repo.
// The resolver is stateless — no caching, identical to how mention-suggestions
// re-reads the host yaml on every call.
package service

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	agentsessiondomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
	skilldomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/skill/domain"
)

// Resolver implements skilldomain.Resolver.
type Resolver struct {
	blob agentsessiondomain.HostBlobReader
}

type ResolverDeps struct {
	Blob agentsessiondomain.HostBlobReader
}

func NewResolver(deps *ResolverDeps) *Resolver {
	return &Resolver{blob: deps.Blob}
}

// skillsDir is the canonical directory inside a host repo.
const skillsDir = ".hangrix/skills"

// skillFile is the single Markdown file consumed from each skill directory.
const skillFile = "SKILL.md"

// maxDescriptionRunes caps the description returned in List to 200 Unicode
// characters so the frontend never receives an unexpectedly long string.
const maxDescriptionRunes = 200

// List satisfies skilldomain.Resolver.
func (r *Resolver) List(ctx context.Context, repoFsPath, ref string) ([]skilldomain.Skill, error) {
	entries, ok := r.blob.ListBlobs(ctx, repoFsPath, ref, skillsDir)
	if !ok {
		// No .hangrix/skills/ directory — empty list, not an error.
		return []skilldomain.Skill{}, nil
	}
	out := make([]skilldomain.Skill, 0, len(entries))
	for _, p := range entries {
		// Expected layout: .hangrix/skills/<slug>/ and optionally
		// .hangrix/skills/<slug>/SKILL.md. ListBlobs returns direct
		// children of the directory, which may include <slug> itself
		// (a tree entry) or <slug>/SKILL.md. We always resolve to the
		// slug by stripping any trailing filename.
		slug := filepath.Base(strings.TrimSuffix(p, "/"))
		if slug == "." || slug == "" {
			continue
		}
		if !skilldomain.SkillSlugRe.MatchString(slug) {
			continue
		}
		sk, err := r.Load(ctx, repoFsPath, ref, slug)
		if err != nil {
			log.Printf("skill: list skipping %q: %v", slug, err)
			continue
		}
		if utf8.RuneCountInString(sk.Description) > maxDescriptionRunes {
			sk.Description = truncateRunes(sk.Description, maxDescriptionRunes)
		}
		out = append(out, *sk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}

// Load satisfies skilldomain.Resolver.
func (r *Resolver) Load(ctx context.Context, repoFsPath, ref, slug string) (*skilldomain.Skill, error) {
	if !skilldomain.SkillSlugRe.MatchString(slug) {
		return nil, fmt.Errorf("%w: invalid slug %q", skilldomain.ErrSkillNotFound, slug)
	}
	body, ok := r.blob.ReadBlob(ctx, repoFsPath, ref, filepath.Join(skillsDir, slug, skillFile))
	if !ok {
		return nil, skilldomain.ErrSkillNotFound
	}
	fm, md, err := splitFrontMatter(string(body))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", skilldomain.ErrSkillInvalid, err)
	}
	var w struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := parseYAMLFrontMatter(fm, &w); err != nil {
		return nil, fmt.Errorf("%w: %v", skilldomain.ErrSkillInvalid, err)
	}
	name := w.Name
	if name == "" {
		name = slug
	}
	return &skilldomain.Skill{
		Slug:        slug,
		Name:        name,
		Description: w.Description,
		Body:        strings.TrimSpace(md),
	}, nil
}

// truncateRunes returns s truncated to at most n Unicode characters.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n])
}
