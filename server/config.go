package main

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// config is the operator settings from manifest.yaml's config_schema. kandev
// restarts the plugin when they are saved, so it is read once at startup.
type config struct {
	BotToken           string
	AllowedUserIDs     []int64
	KandevURL          string
	TriageAgentProfile string
	StartAgent         bool
	AlertInteractions  bool
	AlertCompleted     bool
	AlertFailed        bool
	AlertReview        bool
}

// loadConfig parses the raw GetConfig map. Defaults mirror the manifest
// because a field the operator never saved is absent from the map.
func loadConfig(raw map[string]any) (config, error) {
	cfg := config{
		BotToken:           configString(raw, "bot_token"),
		TriageAgentProfile: configString(raw, "triage_agent_profile"),
		StartAgent:         configBool(raw, "start_agent", false),
		AlertInteractions:  configBool(raw, "alert_interactions", true),
		AlertCompleted:     configBool(raw, "alert_completed", true),
		AlertFailed:        configBool(raw, "alert_failed", true),
		AlertReview:        configBool(raw, "alert_review", false),
	}

	ids, err := parseUserIDs(configString(raw, "allowed_user_ids"))
	if err != nil {
		return config{}, err
	}
	cfg.AllowedUserIDs = ids

	base, err := parseKandevURL(configString(raw, "kandev_url"))
	if err != nil {
		return config{}, err
	}
	cfg.KandevURL = base
	return cfg, nil
}

// missingSettings lists the settings the bot cannot run without.
func (c config) missingSettings() []string {
	var missing []string
	if c.BotToken == "" {
		missing = append(missing, "bot_token")
	}
	if c.KandevURL == "" {
		missing = append(missing, "kandev_url")
	}
	return missing
}

func (c config) isAllowed(userID int64) bool {
	for _, id := range c.AllowedUserIDs {
		if id == userID {
			return true
		}
	}
	return false
}

func parseUserIDs(raw string) ([]int64, error) {
	var ids []int64
	seen := map[int64]bool{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("allowed_user_ids: %q is not a Telegram user id", part)
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func parseKandevURL(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("kandev_url: %q must be an http(s) URL such as https://kandev.example", raw)
	}
	return strings.TrimRight(raw, "/"), nil
}

func configString(raw map[string]any, key string) string {
	s, _ := raw[key].(string)
	return strings.TrimSpace(s)
}

func configBool(raw map[string]any, key string, fallback bool) bool {
	if b, ok := raw[key].(bool); ok {
		return b
	}
	return fallback
}
