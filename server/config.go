package main

import (
	"errors"
	"fmt"
	"net"
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
	AlertColumnMoves   bool
	// QuietHours is nil when quiet hours are off.
	QuietHours *quietHours
	// AlertWorkflows are lowercased workflow names; empty means all.
	AlertWorkflows []string
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
		AlertColumnMoves:   configBool(raw, "alert_column_moves", false),
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

	quiet, err := parseQuietHours(configString(raw, "quiet_hours"), configString(raw, "timezone"))
	if err != nil {
		return config{}, err
	}
	cfg.QuietHours = quiet
	cfg.AlertWorkflows = parseWorkflowFilter(configString(raw, "alert_workflows"))
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
	entry := 0
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		entry++
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			// Never echo the entry: a bot token pasted here by mistake would
			// reach the settings card and bot_status.
			return nil, fmt.Errorf("allowed_user_ids: entry %d is not a numeric Telegram user id", entry)
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
	// Errors name what is wrong and never echo raw: it may carry credentials,
	// and the message reaches the settings card and bot_status.
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return "", errors.New("kandev_url: is not a valid URL, use one such as https://kandev.example")
	case u.Scheme != "http" && u.Scheme != "https":
		return "", errors.New("kandev_url: the scheme must be http or https, such as https://kandev.example")
	case u.Host == "":
		return "", errors.New("kandev_url: must include a host, such as https://kandev.example")
	// Credentials would end up in every alert button; a query or fragment
	// would break the /t/<id> links appended to the base.
	case u.User != nil:
		return "", errors.New("kandev_url: must not contain a user name or password")
	case u.RawQuery != "" || u.ForceQuery:
		return "", errors.New("kandev_url: must not contain a query string")
	case u.Fragment != "" || strings.Contains(raw, "#"):
		return "", errors.New("kandev_url: must not contain a fragment")
	}
	return strings.TrimRight(raw, "/"), nil
}

// warnings lists settings that are valid but probably not what the operator
// wants. They are shown in the status card, never treated as errors.
func (c config) warnings() []string {
	if c.KandevURL == "" {
		return nil
	}
	u, err := url.Parse(c.KandevURL)
	if err != nil {
		return nil
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback()) {
		return []string{"kandev_url points at this computer, so links will not open on a phone and Telegram may refuse the alert buttons. Use an address the phone can reach, such as a Tailscale name."}
	}
	return nil
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
