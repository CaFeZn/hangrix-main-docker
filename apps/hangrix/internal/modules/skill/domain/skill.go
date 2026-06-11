// Package domain declares the skill module's external contract: the Skill
// value type, the Resolver interface, and sentinel errors. Skill files live
// under .hangrix/skills/<slug>/SKILL.md in a host repo and are read from the
// default branch tip via git cat-file (concrete impl in service/resolver.go).
// No database tables — skills are repo assets.
package domain

import (
	"context"
	"errors"
	"regexp"
)

// SkillSlugRe matches a valid skill directory name: 1–64 characters from
// [a-z0-9._-], must start with a lower-case letter or digit. This guards
// against accidental directory traversal or shell-metachar confusion.
var SkillSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Skill is one parsed .hangrix/skills/<slug>/SKILL.md entry.
type Skill struct {
	Slug        string // directory name, matches SkillSlugRe
	Name        string // frontmatter "name", falls back to Slug when empty
	Description string // frontmatter "description", truncated to ≤200 runes on list
	Body        string // Markdown body (trimmed)
}

// Resolver reads skills from a bare repo at a given ref. Consumers depend on
// this interface via ioc; the concrete implementation lives in service/ and
// depends on agent_session/domain.HostBlobReader.
type Resolver interface {
	// List returns every valid skill under .hangrix/skills/ at the given ref.
	// Parsing failures on individual skills are skipped with a warn log so the
	// dropdown stays functional. Returns (nil, error) only on infrastructure
	// failures.
	List(ctx context.Context, repoFsPath, ref string) ([]Skill, error)

	// Load reads and parses a single SKILL.md. Returns ErrSkillNotFound when
	// the directory or file is missing; ErrSkillInvalid when frontmatter is
	// malformed.
	Load(ctx context.Context, repoFsPath, ref, slug string) (*Skill, error)
}

var (
	ErrSkillNotFound = errors.New("skill: not found")
	ErrSkillInvalid  = errors.New("skill: invalid SKILL.md")
)
