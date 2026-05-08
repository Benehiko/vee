package vm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const truenasAPITimeout = 10 * time.Second

// EnsureTrueNASAPIKey checks if cfg already has an API key; if not, creates one
// via the TrueNAS REST API using Basic auth (prompts for password via the provided
// promptFn), persists it to vm.yaml, and returns the key.
func EnsureTrueNASAPIKey(cfg *VMConfig, ip, storagePath string, promptFn func(prompt string) (string, error)) (string, error) {
	if cfg.TrueNASAPIKey != "" {
		return cfg.TrueNASAPIKey, nil
	}

	password, err := promptFn("TrueNAS root password (to create a vee API key): ")
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}

	client := &http.Client{Timeout: truenasAPITimeout}
	base := "http://" + ip + "/api/v2.0"

	apiKey, err := truenasCreateAPIKey(client, base, "root", password)
	if err != nil {
		return "", fmt.Errorf("create TrueNAS API key: %w", err)
	}

	cfg.TrueNASAPIKey = apiKey
	if err := SaveConfig(storagePath, cfg); err != nil {
		return "", fmt.Errorf("save vm.yaml with API key: %w", err)
	}
	return apiKey, nil
}

// InjectVeeSSHKey adds pubKey to the root user's authorized SSH keys on a
// TrueNAS SCALE instance reachable at ip, authenticated with apiKey.
// The operation is idempotent — if the key is already present it does nothing.
func InjectVeeSSHKey(ip, apiKey, pubKey string) error {
	client := &http.Client{Timeout: truenasAPITimeout}
	base := "http://" + ip + "/api/v2.0"
	auth := "Bearer " + apiKey

	userID, currentKeys, err := truenasFindUser(client, base, auth, "root")
	if err != nil {
		return fmt.Errorf("truenas find user: %w", err)
	}

	pubKey = strings.TrimSpace(pubKey)
	if strings.Contains(currentKeys, pubKey) {
		return nil
	}

	merged := strings.TrimSpace(currentKeys)
	if merged != "" {
		merged += "\n"
	}
	merged += pubKey + "\n"

	return truenasUpdateSSHKey(client, base, auth, userID, merged)
}

func truenasCreateAPIKey(client *http.Client, base, user, password string) (string, error) {
	payload, err := json.Marshal(map[string]any{
		"name":      "vee",
		"allowlist": []any{},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest("POST", base+"/api_key", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(user, password)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("POST /api_key: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("POST /api_key: status %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parse api_key response: %w", err)
	}
	if result.Key == "" {
		return "", fmt.Errorf("TrueNAS returned empty API key")
	}
	return result.Key, nil
}

func truenasFindUser(client *http.Client, base, auth, username string) (int, string, error) {
	req, err := http.NewRequest("GET", base+"/user?username="+username, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Authorization", auth)

	resp, err := client.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("GET /user: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return 0, "", fmt.Errorf("GET /user: status %d: %s", resp.StatusCode, body)
	}

	var users []struct {
		ID        int    `json:"id"`
		Username  string `json:"username"`
		SSHPubKey string `json:"sshpubkey"`
	}
	if err := json.Unmarshal(body, &users); err != nil {
		return 0, "", fmt.Errorf("parse user list: %w", err)
	}
	for _, u := range users {
		if u.Username == username {
			return u.ID, u.SSHPubKey, nil
		}
	}
	return 0, "", fmt.Errorf("user %q not found", username)
}

func truenasUpdateSSHKey(client *http.Client, base, auth string, userID int, sshpubkey string) error {
	payload, err := json.Marshal(map[string]string{"sshpubkey": sshpubkey})
	if err != nil {
		return err
	}
	req, err := http.NewRequest("PUT", fmt.Sprintf("%s/user/id/%d", base, userID), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("PUT /user/id/%d: %w", userID, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("PUT /user/id/%d: status %d: %s", userID, resp.StatusCode, body)
	}
	return nil
}
