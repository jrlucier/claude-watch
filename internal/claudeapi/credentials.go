package claudeapi

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// CredentialsPath returns the location of Claude's OAuth credentials file.
// Honours $CLAUDE_CONFIG_DIR (matching the upstream `claude` CLI and the
// Haletran extension), falling back to ~/.claude/.credentials.json.
func CredentialsPath() (string, error) {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, ".credentials.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", ".credentials.json"), nil
}

// credentialsFile mirrors the relevant subset of ~/.claude/.credentials.json.
//
//	{ "claudeAiOauth": { "accessToken": "..." , ... }, ... }
type credentialsFile struct {
	ClaudeAiOauth struct {
		AccessToken string `json:"accessToken"`
	} `json:"claudeAiOauth"`
}

// ReadToken reads the OAuth access token from the credentials file.
// Returns an empty string with a non-nil error when the file is missing,
// malformed, or carries no token.
func ReadToken() (string, error) {
	p, err := CredentialsPath()
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("read credentials %s: %w", p, err)
	}
	var c credentialsFile
	if err := json.Unmarshal(b, &c); err != nil {
		return "", fmt.Errorf("parse credentials %s: %w", p, err)
	}
	tok := c.ClaudeAiOauth.AccessToken
	if tok == "" {
		return "", fmt.Errorf("credentials %s has no claudeAiOauth.accessToken", p)
	}
	return tok, nil
}
