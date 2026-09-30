package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Config is read from the environment (systemd EnvironmentFile in production).
type Config struct {
	Listen string // e.g. 127.0.0.1:8080

	// Meta / WhatsApp Cloud API
	WAToken         string // system user permanent token
	WAAppSecret     string // for X-Hub-Signature-256
	WAVerifyToken   string // webhook verification handshake
	WAPhoneNumberID string // sending number
	GraphVersion    string // e.g. v23.0

	// Who may talk to the assistant. WA ID is the phone number in E.164
	// digits without "+". UserID is the business-scoped user id (BSUID)
	// Meta includes in webhooks; optional but recommended.
	AllowedWAID   string
	AllowedUserID string

	// Vault and git
	VaultDir string // /srv/vault
	GitDir   string // /srv/vault-git
	MediaDir string // where inbound images land, relative to VaultDir
	GitPush  bool

	// Claude
	ClaudeRun     string // /home/assistant/bin/claude-run
	ClaudeUser    string // assistant
	ClaudeTimeout time.Duration

	DBPath string
}

func loadConfig() (*Config, error) {
	cfg := &Config{
		Listen:          env("LISTEN", "127.0.0.1:8080"),
		WAToken:         os.Getenv("WA_TOKEN"),
		WAAppSecret:     os.Getenv("WA_APP_SECRET"),
		WAVerifyToken:   os.Getenv("WA_VERIFY_TOKEN"),
		WAPhoneNumberID: os.Getenv("WA_PHONE_NUMBER_ID"),
		GraphVersion:    env("GRAPH_VERSION", "v23.0"),
		AllowedWAID:     os.Getenv("ALLOWED_WA_ID"),
		AllowedUserID:   os.Getenv("ALLOWED_USER_ID"),
		VaultDir:        env("VAULT_DIR", "/srv/vault"),
		GitDir:          env("GIT_DIR", "/srv/vault-git"),
		MediaDir:        env("MEDIA_DIR", "_assets/whatsapp"),
		GitPush:         env("GIT_PUSH", "true") == "true",
		ClaudeRun:       env("CLAUDE_RUN", "/home/assistant/bin/claude-run"),
		ClaudeUser:      env("CLAUDE_USER", "assistant"),
		DBPath:          env("DB_PATH", "/var/lib/personal-assistant/bridge.db"),
	}

	timeout, err := time.ParseDuration(env("CLAUDE_TIMEOUT", "10m"))
	if err != nil {
		return nil, fmt.Errorf("CLAUDE_TIMEOUT: %w", err)
	}
	cfg.ClaudeTimeout = timeout

	var missing []string
	for name, v := range map[string]string{
		"WA_TOKEN":           cfg.WAToken,
		"WA_APP_SECRET":      cfg.WAAppSecret,
		"WA_VERIFY_TOKEN":    cfg.WAVerifyToken,
		"WA_PHONE_NUMBER_ID": cfg.WAPhoneNumberID,
		"ALLOWED_WA_ID":      cfg.AllowedWAID,
	} {
		if strings.TrimSpace(v) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required env: %s", strings.Join(missing, ", "))
	}
	return cfg, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
