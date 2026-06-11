package agentsconfig

import (
	"errors"
	"reflect"
	"testing"
)

func TestBuildSilence_Happy(t *testing.T) {
	t.Parallel()

	w := &silenceWire{
		Schedules: []silenceScheduleWire{
			{
				Name:     "nightly",
				Cron:     "0 22 * * 1-5",
				Duration: "10h",
				Timezone: "Asia/Shanghai",
			},
			{
				Name:     "weekend",
				Cron:     "0 22 * * 5",
				Duration: "58h",
				Timezone: "", // defaults to UTC
			},
		},
	}

	cfg, err := buildSilence(w)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if len(cfg.Schedules) != 2 {
		t.Fatalf("got %d schedules, want 2", len(cfg.Schedules))
	}

	s0 := cfg.Schedules[0]
	if s0.Name != "nightly" {
		t.Fatalf("s0.Name = %q, want nightly", s0.Name)
	}
	if s0.Cron != "0 22 * * 1-5" {
		t.Fatalf("s0.Cron = %q", s0.Cron)
	}
	if s0.Duration != "10h" {
		t.Fatalf("s0.Duration = %q", s0.Duration)
	}
	if s0.Timezone != "Asia/Shanghai" {
		t.Fatalf("s0.Timezone = %q, want Asia/Shanghai", s0.Timezone)
	}

	s1 := cfg.Schedules[1]
	if s1.Timezone != "UTC" {
		t.Fatalf("s1.Timezone = %q, want UTC (default)", s1.Timezone)
	}
}

func TestBuildSilence_Nil(t *testing.T) {
	t.Parallel()

	cfg, err := buildSilence(nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected nil config for absent silence block, got %+v", cfg)
	}
}

func TestBuildSilence_EmptySchedules(t *testing.T) {
	t.Parallel()

	w := &silenceWire{}
	cfg, err := buildSilence(w)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config, nil means block absent")
	}
	if len(cfg.Schedules) != 0 {
		t.Fatalf("expected 0 schedules, got %d", len(cfg.Schedules))
	}
}

func TestBuildSilence_Errors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		wire    *silenceWire
		substr  string // expected in error message
	}{
		{
			name: "empty-name",
			wire: &silenceWire{
				Schedules: []silenceScheduleWire{
					{Name: "", Cron: "0 0 * * *", Duration: "1h"},
				},
			},
			substr: "is empty",
		},
		{
			name: "bad-name-pattern",
			wire: &silenceWire{
				Schedules: []silenceScheduleWire{
					{Name: "Bad_Name", Cron: "0 0 * * *", Duration: "1h"},
				},
			},
			substr: "must match",
		},
		{
			name: "duplicate-name",
			wire: &silenceWire{
				Schedules: []silenceScheduleWire{
					{Name: "dup", Cron: "0 0 * * *", Duration: "1h"},
					{Name: "dup", Cron: "1 0 * * *", Duration: "2h"},
				},
			},
			substr: "duplicate",
		},
		{
			name: "empty-cron",
			wire: &silenceWire{
				Schedules: []silenceScheduleWire{
					{Name: "s1", Cron: "", Duration: "1h"},
				},
			},
			substr: "cron is empty",
		},
		{
			name: "invalid-cron",
			wire: &silenceWire{
				Schedules: []silenceScheduleWire{
					{Name: "s1", Cron: "not a cron", Duration: "1h"},
				},
			},
			substr: "cron",
		},
		{
			name: "empty-duration",
			wire: &silenceWire{
				Schedules: []silenceScheduleWire{
					{Name: "s1", Cron: "0 0 * * *", Duration: ""},
				},
			},
			substr: "duration is empty",
		},
		{
			name: "invalid-duration",
			wire: &silenceWire{
				Schedules: []silenceScheduleWire{
					{Name: "s1", Cron: "0 0 * * *", Duration: "xyz"},
				},
			},
			substr: "duration",
		},
		{
			name: "negative-duration",
			wire: &silenceWire{
				Schedules: []silenceScheduleWire{
					{Name: "s1", Cron: "0 0 * * *", Duration: "-1h"},
				},
			},
			substr: "must be positive",
		},
		{
			name: "invalid-timezone",
			wire: &silenceWire{
				Schedules: []silenceScheduleWire{
					{Name: "s1", Cron: "0 0 * * *", Duration: "1h", Timezone: "Mars/Base"},
				},
			},
			substr: "timezone",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildSilence(tc.wire)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, ErrInvalidSilence) {
				t.Fatalf("got %v, want errors.Is ErrInvalidSilence", err)
			}
			if !stringsContains(err.Error(), tc.substr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.substr)
			}
		})
	}
}

func TestSilenceRoundTrip(t *testing.T) {
	t.Parallel()

	// Parse with silence block integrated through ParseHostConfig.
	yaml := `version: 1
container:
  image: test:1
silence:
  schedules:
    - name: nightly
      cron: "0 22 * * 1-5"
      duration: "10h"
      timezone: "Asia/Shanghai"
`

	cfg, err := ParseHostConfig([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg.Silence == nil {
		t.Fatal("expected non-nil Silence config")
	}
	if len(cfg.Silence.Schedules) != 1 {
		t.Fatalf("got %d schedules, want 1", len(cfg.Silence.Schedules))
	}
	s := cfg.Silence.Schedules[0]
	if s.Name != "nightly" {
		t.Fatalf("name = %q", s.Name)
	}
	if s.Cron != "0 22 * * 1-5" {
		t.Fatalf("cron = %q", s.Cron)
	}
	if s.Duration != "10h" {
		t.Fatalf("duration = %q", s.Duration)
	}
	if s.Timezone != "Asia/Shanghai" {
		t.Fatalf("timezone = %q", s.Timezone)
	}
}

func TestSilenceRoundTrip_NoSilenceBlock(t *testing.T) {
	t.Parallel()

	yaml := `version: 1
container:
  image: test:1
`

	cfg, err := ParseHostConfig([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg.Silence != nil {
		t.Fatalf("expected nil Silence, got %+v", cfg.Silence)
	}
}

func TestBuildSilence_AllErrors(t *testing.T) {
	t.Parallel()

	// Trigger multiple errors and ensure we still get ErrInvalidSilence.
	w := &silenceWire{
		Schedules: []silenceScheduleWire{
			{Name: "", Cron: "", Duration: ""},
		},
	}
	_, err := buildSilence(w)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrInvalidSilence) {
		t.Fatalf("got %v, want ErrInvalidSilence", err)
	}
}

func stringsContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func requireEqual[T comparable](t *testing.T, got, want T, msg string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: got %v, want %v", msg, got, want)
	}
}

var _ = requireEqual[string]
