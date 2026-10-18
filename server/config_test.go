package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadConfig_Defaults(t *testing.T) {
	cfg, err := loadConfig(map[string]any{})
	require.NoError(t, err)
	require.Empty(t, cfg.BotToken)
	require.Empty(t, cfg.AllowedUserIDs)
	require.False(t, cfg.StartAgent)
	require.True(t, cfg.AlertInteractions)
	require.True(t, cfg.AlertCompleted)
	require.True(t, cfg.AlertFailed)
	require.False(t, cfg.AlertReview)
	require.ElementsMatch(t, []string{"bot_token", "kandev_url"}, cfg.missingSettings())
}

func TestLoadConfig_AllFields(t *testing.T) {
	cfg, err := loadConfig(map[string]any{
		"bot_token":            " 123:abc ",
		"allowed_user_ids":     " 42, 7 ,,1001 ",
		"kandev_url":           "https://kandev.tail.ts.net:38429/",
		"triage_agent_profile": "profile-1",
		"start_agent":          true,
		"alert_interactions":   false,
		"alert_completed":      false,
		"alert_failed":         false,
		"alert_review":         true,
	})
	require.NoError(t, err)
	require.Equal(t, "123:abc", cfg.BotToken)
	require.Equal(t, []int64{42, 7, 1001}, cfg.AllowedUserIDs)
	require.Equal(t, "https://kandev.tail.ts.net:38429", cfg.KandevURL)
	require.Equal(t, "profile-1", cfg.TriageAgentProfile)
	require.True(t, cfg.StartAgent)
	require.False(t, cfg.AlertInteractions)
	require.False(t, cfg.AlertCompleted)
	require.False(t, cfg.AlertFailed)
	require.True(t, cfg.AlertReview)
	require.Empty(t, cfg.missingSettings())
}

func TestLoadConfig_DeduplicatesUserIDs(t *testing.T) {
	cfg, err := loadConfig(map[string]any{"allowed_user_ids": "42,42, 7"})
	require.NoError(t, err)
	require.Equal(t, []int64{42, 7}, cfg.AllowedUserIDs)
}

func TestLoadConfig_RejectsInvalidUserID(t *testing.T) {
	for _, raw := range []string{"42,abc", "-5", "0", "12.5", "@someone"} {
		_, err := loadConfig(map[string]any{"allowed_user_ids": raw})
		require.Error(t, err, raw)
		require.Contains(t, err.Error(), "allowed_user_ids")
	}
}

func TestLoadConfig_RejectsInvalidKandevURL(t *testing.T) {
	for _, raw := range []string{"kandev.local", "ftp://kandev.local", "http://", "://x"} {
		_, err := loadConfig(map[string]any{"kandev_url": raw})
		require.Error(t, err, raw)
		require.Contains(t, err.Error(), "kandev_url")
	}
}

func TestLoadConfig_RejectsKandevURLWithCredentialsQueryOrFragment(t *testing.T) {
	for _, raw := range []string{
		"https://user:s3cret@kandev.example",
		"https://user@kandev.example",
		"https://kandev.example/?tab=1",
		"https://kandev.example/#inbox",
	} {
		_, err := loadConfig(map[string]any{"kandev_url": raw})
		require.Error(t, err, raw)
		require.Contains(t, err.Error(), "kandev_url")
		require.NotContains(t, err.Error(), "s3cret", "the error never echoes credentials")
	}
}

func TestConfig_WarnsOnLoopbackKandevURL(t *testing.T) {
	for _, raw := range []string{"http://localhost:3000", "http://127.0.0.1:3000/", "http://[::1]:3000", "http://LocalHost"} {
		cfg, err := loadConfig(map[string]any{"kandev_url": raw})
		require.NoError(t, err, raw)
		warnings := cfg.warnings()
		require.Len(t, warnings, 1, raw)
		require.Contains(t, warnings[0], "kandev_url")
	}
	for _, raw := range []string{"", "https://kandev.tail.ts.net", "http://192.168.1.20:3000"} {
		cfg, err := loadConfig(map[string]any{"kandev_url": raw})
		require.NoError(t, err, raw)
		require.Empty(t, cfg.warnings(), raw)
	}
}

func TestConfig_IsAllowed(t *testing.T) {
	cfg := config{AllowedUserIDs: []int64{42}}
	require.True(t, cfg.isAllowed(42))
	require.False(t, cfg.isAllowed(43))
	require.False(t, config{}.isAllowed(42), "an empty allowlist answers nobody")
}
