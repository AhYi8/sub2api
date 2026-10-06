package service

import (
	"context"
	"testing"
)

type tempUnschedulablePolicyRepoStub struct {
	policy *TempUnschedulablePolicy
}

func (s tempUnschedulablePolicyRepoStub) GetTempUnschedulablePolicy(_ context.Context, _ string) (*TempUnschedulablePolicy, error) {
	return s.policy, nil
}

func (tempUnschedulablePolicyRepoStub) SetTempUnschedulablePolicy(context.Context, *TempUnschedulablePolicy) error {
	return nil
}

func TestEffectiveTempUnschedulableRulesModeMatrix(t *testing.T) {
	rules := []TempUnschedulableRule{{ErrorCode: 429, Keywords: []string{"busy"}, DurationMinutes: 2}}
	rawRules := []any{map[string]any{"error_code": float64(429), "keywords": []any{"busy"}, "duration_minutes": float64(2)}}
	cases := []struct {
		name    string
		creds   map[string]any
		wantOn  bool
		wantLen int
	}{
		{"explicit disabled", map[string]any{"temp_unschedulable_mode": "disabled"}, false, 0},
		{"explicit inherit", map[string]any{"temp_unschedulable_mode": "inherit"}, true, 1},
		{"explicit override", map[string]any{"temp_unschedulable_mode": "override", "temp_unschedulable_enabled": true, "temp_unschedulable_rules": rawRules}, true, 1},
		{"override after legacy cleanup", map[string]any{"temp_unschedulable_mode": "override", "temp_unschedulable_rules": rawRules}, true, 1},
		{"legacy enabled", map[string]any{"temp_unschedulable_enabled": true, "temp_unschedulable_rules": rawRules}, true, 1},
		{"legacy disabled", map[string]any{"temp_unschedulable_enabled": false, "temp_unschedulable_rules": rules}, false, 0},
		{"missing legacy", map[string]any{}, true, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{Platform: "openai", Credentials: tc.creds}
			got, enabled := EffectiveTempUnschedulableRules(context.Background(), tempUnschedulablePolicyRepoStub{policy: &TempUnschedulablePolicy{Enabled: true, Rules: rules}}, account)
			if enabled != tc.wantOn || len(got) != tc.wantLen {
				t.Fatalf("enabled=%v rules=%d, want enabled=%v rules=%d", enabled, len(got), tc.wantOn, tc.wantLen)
			}
		})
	}
}

func TestEffectiveTempUnschedulableRulesWithoutPlatformRepository(t *testing.T) {
	rawRules := []any{map[string]any{"error_code": float64(429), "keywords": []any{"busy"}, "duration_minutes": float64(2)}}
	for _, tc := range []struct {
		name   string
		creds  map[string]any
		on     bool
		length int
	}{
		{name: "disabled", creds: map[string]any{"temp_unschedulable_mode": "disabled", "temp_unschedulable_enabled": true, "temp_unschedulable_rules": rawRules}},
		{name: "override", creds: map[string]any{"temp_unschedulable_mode": "override", "temp_unschedulable_enabled": true, "temp_unschedulable_rules": rawRules}, on: true, length: 1},
		{name: "legacy", creds: map[string]any{"temp_unschedulable_enabled": true, "temp_unschedulable_rules": rawRules}, on: true, length: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, enabled := EffectiveTempUnschedulableRules(context.Background(), nil, &Account{Credentials: tc.creds})
			if enabled != tc.on || len(got) != tc.length {
				t.Fatalf("enabled=%v rules=%d, want enabled=%v rules=%d", enabled, len(got), tc.on, tc.length)
			}
		})
	}
}

func TestApplyTempUnschedulableModeDefaults(t *testing.T) {
	got := ApplyTempUnschedulableMode(map[string]any{"api_key": "keep"}, "", true)
	if got["temp_unschedulable_mode"] != "disabled" || got["api_key"] != "keep" {
		t.Fatalf("unexpected new-account credentials: %#v", got)
	}
	legacy := ApplyTempUnschedulableMode(map[string]any{"temp_unschedulable_enabled": true}, "", true)
	if legacy["temp_unschedulable_mode"] != "override" || legacy["temp_unschedulable_enabled"] != true {
		t.Fatalf("legacy enabled credentials must remain override-compatible: %#v", legacy)
	}
	fresh := ApplyTempUnschedulableMode(map[string]any{"api_key": "keep"}, "", true)
	if fresh["temp_unschedulable_mode"] != "disabled" {
		t.Fatalf("new account without legacy configuration must default to disabled: %#v", fresh)
	}
}
