// internal/mailcow/mailcow.go
package mailcow

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"exim2sieve/internal/config"
)

type Client struct {
	apiURL  string
	apiKey  string
	quotaMB int
	http    *http.Client
}

func NewClientFromConfig(cfg *config.Config) (*Client, error) {
	if cfg.MailcowAPIURL == "" || cfg.MailcowAPIKey == "" {
		return nil, fmt.Errorf("mailcow api_url or api_key not configured")
	}

	return &Client{
		apiURL:  strings.TrimRight(cfg.MailcowAPIURL, "/"),
		apiKey:  cfg.MailcowAPIKey,
		quotaMB: cfg.MailcowQuotaMB,
		http: &http.Client{
			Timeout: 15 * time.Second,
		},
	}, nil
}

// EnsureDomain tries to create the domain. If it already exists,
// we just log and continue.
func (c *Client) EnsureDomain(domain string) error {
	endpoint := c.apiURL + "/add/domain"

	payload := map[string]interface{}{
		"domain":   domain,
		"active":   "1",
		"backupmx": "0",
		// Keep quotas mostly unlimited / default – can be tuned later.
		"max_quota":   "0",
		"max_mailboxes": "0",
		"def_quota":   fmt.Sprintf("%d", c.quotaMB),
		"quota":       "0",
	}

	respBody, status, err := c.postJSON(endpoint, payload)
	if err != nil {
		return fmt.Errorf("EnsureDomain %s: %w", domain, err)
	}

	if status/100 != 2 {
		// Mailcow often returns messages like "Domain already exists"
		// in the body; log but don't fail hard.
		log.Printf("mailcow: add/domain %s returned HTTP %d: %s", domain, status, string(respBody))
		return nil
	}

	return nil
}

// CreateMailbox creates a mailbox via mailcow API.
// It does NOT treat "already exists" specially — that will appear
// in the logs, but processing will continue.
func (c *Client) CreateMailbox(localPart, domain, name, password string) error {
	endpoint := c.apiURL + "/add/mailbox"

	if name == "" {
		name = localPart
	}

	payload := map[string]interface{}{
		"local_part":      localPart,
		"domain":          domain,
		"name":            name,
		"quota":           fmt.Sprintf("%d", c.quotaMB),
		"password":        password,
		"password2":       password,
		"active":          "1",
		"force_pw_update": "0",
		"tls_enforce_in":  "0",
		"tls_enforce_out": "0",
	}

	respBody, status, err := c.postJSON(endpoint, payload)
	if err != nil {
		return fmt.Errorf("CreateMailbox %s@%s: %w", localPart, domain, err)
	}

	if status/100 != 2 {
		// Log body so we can see "mailbox exists" or other errors.
		return fmt.Errorf("CreateMailbox %s@%s: HTTP %d: %s", localPart, domain, status, string(respBody))
	}

	return nil
}

func (c *Client) postJSON(url string, payload interface{}) ([]byte, int, error) {
	buf := &bytes.Buffer{}
	if err := json.NewEncoder(buf).Encode(payload); err != nil {
		return nil, 0, fmt.Errorf("encode json: %w", err)
	}

	req, err := http.NewRequest("POST", url, buf)
	if err != nil {
		return nil, 0, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	return body, resp.StatusCode, nil
}

// GeneratePassword returns a random password of the given length.
func GeneratePassword(length int) (string, error) {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*-_"
	if length <= 0 {
		length = 16
	}
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand.Read: %w", err)
	}
	for i := range b {
		b[i] = letters[int(b[i])%len(letters)]
	}
	return string(b), nil
}

// CreateMailboxesFromBackup walks a backup tree (as produced by -cpanel-user)
// and creates mailcow mailboxes for each user that has filters.
//
// backupRoot typically looks like:
//   backupRoot/myipgr/myip.gr/chris/chris.sieve
//   backupRoot/myipgr/myip.gr/chris/filter.yaml
//
// If domainFilter is non-empty, only that domain is processed.
func CreateMailboxesFromBackup(c *Client, backupRoot, domainFilter string, logWriter io.Writer) error {
	if logWriter == nil {
		logWriter = os.Stdout
	}
	logger := log.New(logWriter, "", log.LstdFlags)

	// We expect backupRoot = ./backup/myipgr
	entries, err := os.ReadDir(backupRoot)
	if err != nil {
		return fmt.Errorf("read backup root %s: %w", backupRoot, err)
	}

	// Open password log
	pwLogPath := filepath.Join(backupRoot, "mailcow_mailboxes.log")
	pwLogFile, err := os.OpenFile(pwLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return fmt.Errorf("open pw log: %w", err)
	}
	defer pwLogFile.Close()

	pwLogger := log.New(pwLogFile, "", log.LstdFlags)

	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		domain := ent.Name()
		if domainFilter != "" && domain != domainFilter {
			continue
		}

		domainDir := filepath.Join(backupRoot, domain)

		if err := c.EnsureDomain(domain); err != nil {
			logger.Printf("mailcow: failed to ensure domain %s: %v", domain, err)
			// Continue with other domains.
			continue
		}

		userEntries, err := os.ReadDir(domainDir)
		if err != nil {
			logger.Printf("mailcow: read dir %s: %v", domainDir, err)
			continue
		}

		for _, ue := range userEntries {
			if !ue.IsDir() {
				// Skip files like _domain.filter, @pwcache, etc.
				continue
			}
			user := ue.Name()

			// Skip special dirs
			if strings.HasPrefix(user, "@") || strings.HasPrefix(user, "_") {
				continue
			}

			userDir := filepath.Join(domainDir, user)

			// Only create mailboxes that have filters (sieve or yaml or exim text).
			hasFilters := false
			if _, err := os.Stat(filepath.Join(userDir, user+".sieve")); err == nil {
				hasFilters = true
			}
			if _, err := os.Stat(filepath.Join(userDir, "filter.yaml")); err == nil {
				hasFilters = true
			}
			if _, err := os.Stat(filepath.Join(userDir, "filter")); err == nil {
				hasFilters = true
			}
			if !hasFilters {
				continue
			}

			email := fmt.Sprintf("%s@%s", user, domain)
			pw, err := GeneratePassword(18)
			if err != nil {
				logger.Printf("mailcow: generate password for %s: %v", email, err)
				continue
			}

			if err := c.CreateMailbox(user, domain, user, pw); err != nil {
				logger.Printf("mailcow: create mailbox %s: %v", email, err)
				// Keep going; maybe some already exist.
				continue
			}

			logger.Printf("mailcow: created mailbox %s (quota=%dMB)", email, c.quotaMB)
			pwLogger.Printf("%s; %s; %s; %s; %s",
				time.Now().UTC().Format(time.RFC3339),
				domain,
				user,
				email,
				pw,
			)
		}
	}

	return nil
}
