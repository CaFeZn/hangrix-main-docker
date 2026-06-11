package agentsconfig

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// SilenceConfig models the optional top-level `silence:` block in
// `.hangrix/agents.yml`. nil means the repo has no silence schedules
// configured — the SilenceGate is free (always pass).
type SilenceConfig struct {
	// Schedules is the ordered list of time-based silence windows.
	// All items are OR-combined (any match → silent). The list
	// may be empty (no schedules, only manual silence via API).
	Schedules []SilenceSchedule
}

// SilenceSchedule is one cron-driven silence window definition.
// It declares when to enter silence (cron) and how long to stay
// silent (duration), in a given timezone.
type SilenceSchedule struct {
	// Name is a human-readable label, also used as source_ref
	// in the audit log. Must be non-empty and unique within the
	// schedules list.
	Name string

	// Cron is a robfig/cron 5-field expression (minute hour
	// day-of-month month day-of-week) specifying when the
	// silence window opens. Required. Seconds are forced to 0.
	Cron string

	// Duration is the silence window length, in
	// time.ParseDuration format (e.g. "10h", "1h30m").
	// Required.
	Duration string

	// Timezone is an IANA timezone name like "Asia/Shanghai".
	// Default "UTC" when empty.
	Timezone string
}

// silenceWire is the yaml wire shape for the `silence:` block.
type silenceWire struct {
	Schedules []silenceScheduleWire `yaml:"schedules"`
}

type silenceScheduleWire struct {
	Name     string `yaml:"name"`
	Cron     string `yaml:"cron"`
	Duration string `yaml:"duration"`
	Timezone string `yaml:"timezone"`
}

// buildSilence validates and lifts the silence wire into a
// SilenceConfig. Returns nil when the block is absent (w == nil),
// so callers can tell "no silence block" from "empty silence block".
func buildSilence(w *silenceWire) (*SilenceConfig, error) {
	if w == nil {
		return nil, nil
	}
	if len(w.Schedules) == 0 {
		return &SilenceConfig{}, nil
	}

	seenNames := make(map[string]bool, len(w.Schedules))
	schedules := make([]SilenceSchedule, 0, len(w.Schedules))

	for i, sw := range w.Schedules {
		field := fmt.Sprintf("silence.schedules[%d]", i)

		// Name: required, unique, [a-z][a-z0-9-]* pattern.
		if strings.TrimSpace(sw.Name) == "" {
			return nil, fmt.Errorf("%w: %s.name is empty", ErrInvalidSilence, field)
		}
		if !isValidScheduleName(sw.Name) {
			return nil, fmt.Errorf("%w: %s.name=%q must match [a-z][a-z0-9-]*", ErrInvalidSilence, field, sw.Name)
		}
		if seenNames[sw.Name] {
			return nil, fmt.Errorf("%w: %s.name=%q is a duplicate", ErrInvalidSilence, field, sw.Name)
		}
		seenNames[sw.Name] = true

		// Cron: required, must be valid 5-field cron.
		if strings.TrimSpace(sw.Cron) == "" {
			return nil, fmt.Errorf("%w: %s.cron is empty", ErrInvalidSilence, field)
		}
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		if _, err := parser.Parse(sw.Cron); err != nil {
			return nil, fmt.Errorf("%w: %s.cron=%q: %s", ErrInvalidSilence, field, sw.Cron, err.Error())
		}

		// Duration: required, valid time.ParseDuration.
		if strings.TrimSpace(sw.Duration) == "" {
			return nil, fmt.Errorf("%w: %s.duration is empty", ErrInvalidSilence, field)
		}
		dur, err := time.ParseDuration(sw.Duration)
		if err != nil {
			return nil, fmt.Errorf("%w: %s.duration=%q: %s", ErrInvalidSilence, field, sw.Duration, err.Error())
		}
		if dur <= 0 {
			return nil, fmt.Errorf("%w: %s.duration=%q must be positive", ErrInvalidSilence, field, sw.Duration)
		}

		// Timezone: optional, defaults to "UTC". Must be valid IANA.
		tz := strings.TrimSpace(sw.Timezone)
		if tz == "" {
			tz = "UTC"
		}
		if _, err := time.LoadLocation(tz); err != nil {
			return nil, fmt.Errorf("%w: %s.timezone=%q: %s", ErrInvalidSilence, field, tz, err.Error())
		}

		schedules = append(schedules, SilenceSchedule{
			Name:     sw.Name,
			Cron:     sw.Cron,
			Duration: sw.Duration,
			Timezone: tz,
		})
	}

	return &SilenceConfig{Schedules: schedules}, nil
}

// isValidScheduleName checks that the schedule name matches
// `^[a-z][a-z0-9-]*$` and is ≤ 100 chars.
func isValidScheduleName(s string) bool {
	if s == "" || len(s) > 100 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		case r == '-' && i > 0:
		default:
			return false
		}
	}
	return true
}
