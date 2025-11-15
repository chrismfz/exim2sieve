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


// mailcowAPIResponse models the standard Mailcow API response:
// [
//   {
//     "type": "success" | "danger" | "error",
//     "msg":  ["something", "detail"],
//     "log":  [...]
//   }
// ]
type mailcowAPIResponse struct {
        Type string            `json:"type"`
        Msg  []string          `json:"msg"`
        Log  json.RawMessage   `json:"log"`
}

func parseMailcowResponse(body []byte) (*mailcowAPIResponse, error) {
        var arr []mailcowAPIResponse
        if err := json.Unmarshal(body, &arr); err != nil {
            // Not fatal – Mailcow sometimes changes formats; caller can
            // still fall back to raw body.
            return nil, err
        }
        if len(arr) == 0 {
            return nil, nil
        }
        return &arr[0], nil
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

        // We'll give the domain a very large quota so default mailbox
        // quotas never exceed the domain quota.
        baseDomainQuota := c.quotaMB * 100 // e.g. 10GB user → 1TB domain

        makePayload := func(quotaMB int) map[string]interface{} {
                return map[string]interface{}{
                        "domain":    domain,
                        "active":    "1",
                        "backupmx":  "0",
                        // Use documented field names (no underscores):
                       // defquota: default mailbox size
                        // maxquota, quota: per-mailbox and total domain quota
                        "defquota":  fmt.Sprintf("%d", c.quotaMB),
                        "quota":     fmt.Sprintf("%d", quotaMB),
                        "maxquota":  fmt.Sprintf("%d", quotaMB),
                        "mailboxes": "0",
                        "aliases":   "400",
                }
        }

        tryOnce := func(quotaMB int) (*mailcowAPIResponse, []byte, int, error) {
                body, status, err := c.postJSON(endpoint, makePayload(quotaMB))
                if err != nil {
                        return nil, body, status, fmt.Errorf("EnsureDomain %s: %w", domain, err)
                }
                if status/100 != 2 {
                        return nil, body, status, fmt.Errorf("EnsureDomain %s: HTTP %d: %s", domain, status, string(body))
                }
                resp, _ := parseMailcowResponse(body)
                return resp, body, status, nil
        }

        resp, body, _, err := tryOnce(baseDomainQuota)
        if err != nil {
                // HTTP-level or network error
                return err
        }

        if resp == nil || resp.Type == "" {
                // Unknown format but HTTP 200 – assume ok, just log raw.
                log.Printf("mailcow: add/domain %s raw response: %s", domain, string(body))
                return nil
        }

        msgJoined := strings.Join(resp.Msg, ",")

        switch resp.Type {
        case "success":
                log.Printf("mailcow: domain %s created/updated (msg=%s)", domain, msgJoined)
                return nil
        case "danger", "error":
                // Allow "already exists" semantics to pass.
                if strings.Contains(msgJoined, "exist") {
                        log.Printf("mailcow: domain %s already exists (%s)", domain, msgJoined)
                        return nil
                }
                // Special-case: mailbox_quota_exceeds_domain_quota
                if strings.Contains(msgJoined, "mailbox_quota_exceeds_domain_quota") {
                        bigQuota := baseDomainQuota * 10
                        log.Printf("mailcow: domain %s quota too low (%s), retrying with quota=%dMB",
                                domain, msgJoined, bigQuota)
                        resp2, body2, _, err2 := tryOnce(bigQuota)
                        if err2 != nil {
                                return fmt.Errorf("EnsureDomain %s retry failed: %w", domain, err2)
                        }
                        if resp2 == nil || resp2.Type == "" {
                                log.Printf("mailcow: domain %s ensured after retry (raw=%s)", domain, string(body2))
                                return nil
                        }
                        msgJoined2 := strings.Join(resp2.Msg, ",")
                        if resp2.Type == "success" || strings.Contains(msgJoined2, "exist") {
                                log.Printf("mailcow: domain %s ensured after retry (msg=%s)", domain, msgJoined2)
                                return nil
                        }
                        return fmt.Errorf("EnsureDomain %s retry failed: type=%s msg=%s body=%s",
                                domain, resp2.Type, msgJoined2, string(body2))
                }
                return fmt.Errorf("EnsureDomain %s failed: type=%s msg=%s body=%s",
                        domain, resp.Type, msgJoined, string(body))
        default:
                // Unexpected type, but don't fail hard.
                log.Printf("mailcow: add/domain %s returned unknown type=%s msg=%s body=%s",
                        domain, resp.Type, msgJoined, string(body))
                return nil
        }

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
                // HTTP-level error
                return fmt.Errorf("CreateMailbox %s@%s: HTTP %d: %s", localPart, domain, status, string(respBody))
        }

        resp, _ := parseMailcowResponse(respBody)
        if resp == nil || resp.Type == "" {
                // Unknown body, treat HTTP 200 as success but log raw.
                log.Printf("mailcow: add/mailbox %s@%s raw response: %s", localPart, domain, string(respBody))
                return nil
        }

        msgJoined := strings.Join(resp.Msg, ",")

        switch resp.Type {
        case "success":
                return nil
        case "danger", "error":
                // Allow "already exists" semantics for idempotency.
                if strings.Contains(msgJoined, "exist") {
                        log.Printf("mailcow: mailbox %s@%s already exists (%s)", localPart, domain, msgJoined)
                        return nil
                }
                return fmt.Errorf("CreateMailbox %s@%s failed: type=%s msg=%s body=%s",
                        localPart, domain, resp.Type, msgJoined, string(respBody))
        default:
                log.Printf("mailcow: add/mailbox %s@%s returned unknown type=%s msg=%s body=%s",
                        localPart, domain, resp.Type, msgJoined, string(respBody))
                return nil
        }


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

                        // Log to stdout with full details (including password)
                        logger.Printf("mailcow: created mailbox %s (quota=%dMB, password=%s)", email, c.quotaMB, pw)

                        // And also log to the mailbox password log file
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
