package service

import (
	"fmt"
	"io"
	"strings"

	"go.yaml.in/yaml/v3"
)

const utf8BOM = "\ufeff"

// splitFrontMatter parses a Markdown file that starts with YAML front matter:
//
//	---
//	name: ...
//	description: ...
//	---
//	...markdown body...
//
// Returns (frontMatterYAML, markdownBody).
func splitFrontMatter(raw string) (string, string, error) {
	s := strings.TrimPrefix(raw, utf8BOM)
	lines := strings.Split(s, "\n")
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i >= len(lines) || strings.TrimSpace(lines[i]) != "---" {
		return "", "", fmt.Errorf("file must start with a '---' front-matter fence")
	}
	start := i + 1
	end := -1
	for j := start; j < len(lines); j++ {
		if strings.TrimSpace(lines[j]) == "---" {
			end = j
			break
		}
	}
	if end < 0 {
		return "", "", fmt.Errorf("unterminated front matter (no closing '---')")
	}
	fm := strings.Join(lines[start:end], "\n")
	body := strings.Join(lines[end+1:], "\n")
	return fm, body, nil
}

func parseYAMLFrontMatter(fm string, out any) error {
	dec := yaml.NewDecoder(strings.NewReader(fm))
	if err := dec.Decode(out); err != nil {
		if err == io.EOF {
			return fmt.Errorf("empty front matter")
		}
		return err
	}
	return nil
}
