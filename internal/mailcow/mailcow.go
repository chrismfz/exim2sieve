// internal/mailcow/mailcow.go
package mailcow

import (
	"bytes"
	"bufio"
	"crypto/rand"
	"database/sql"
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
	 _ "github.com/go-sql-driver/mysql"
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


// apiResponseItem represents a single Mailcow JSON reply object
// e.g. [{"type":"success","msg":["domain_added","example.org"], ...}]
type apiResponseItem struct {
        Type string        `json:"type"`
        Msg  []interface{} `json:"msg"`
}

func parseMailcowResponse(body []byte) (*apiResponseItem, error) {
        var arr []apiResponseItem
        if err := json.Unmarshal(body, &arr); err != nil {
            return nil, err
        }
        if len(arr) == 0 {
            return nil, fmt.Errorf("empty response")
        }
        return &arr[0], nil
}

func joinMsg(msg []interface{}) string {
        if len(msg) == 0 {
                return ""
        }
        parts := make([]string, 0, len(msg))
        for _, m := range msg {
                parts = append(parts, fmt.Sprint(m))
        }
        return strings.Join(parts, ",")
}





// EnsureDomain tries to create the domain. If it already exists,
// we just log and continue.
func (c *Client) EnsureDomain(domain string) error {
	endpoint := c.apiURL + "/add/domain"


        // big enough quota so that per-mailbox quotas never exceed domain quota
        domainQuota := c.quotaMB * 100 // e.g. 10 GB per user → 1 TB domain

        payload := map[string]interface{}{
                "domain":               domain,
                "description":          fmt.Sprintf("Imported from cPanel by exim2sieve for %s", domain),
                "aliases":              "400",
                "mailboxes":            "100", // 0 doesnt means unlimited ffs mailcow
                "defquota":             fmt.Sprintf("%d", c.quotaMB),  // default mailbox quota (MB)
                "maxquota":             fmt.Sprintf("%d", c.quotaMB),  // max quota per mailbox (MB)
                "quota":                fmt.Sprintf("%d", domainQuota),// total domain quota (MB)
                "active":               "1",
                "rl_value":             "0",  // no rate-limit by default
                "rl_frame":             "s",  // per second (if rl_value > 0)
                "backupmx":             "0",
                "relay_all_recipients": "0",
                "restart_sogo":         "1",
        }

        body, status, err := c.postJSON(endpoint, payload)
        if err != nil {
                return fmt.Errorf("EnsureDomain %s: %w", domain, err)
        }

        // HTTP-level error
        if status/100 != 2 {
                return fmt.Errorf("EnsureDomain %s: HTTP %d: %s", domain, status, string(body))
        }

        resp, perr := parseMailcowResponse(body)
        if perr != nil || resp == nil || resp.Type == "" {
                // Unknown / changed format – assume ok, but log raw
                log.Printf("mailcow: add/domain %s raw response: %s", domain, string(body))
                return nil
        }

        msgJoined := joinMsg(resp.Msg)

        switch resp.Type {
        case "success":
                log.Printf("mailcow: domain %s created/updated (msg=%s)", domain, msgJoined)
                return nil
        case "danger", "error":
                if strings.Contains(msgJoined, "exist") {
                        log.Printf("mailcow: domain %s already exists (%s)", domain, msgJoined)
                        return nil
                }
                if strings.Contains(msgJoined, "mailbox_quota_exceeds_domain_quota") {
                        // If ποτέ το καταφέρουμε να το χτυπήσουμε, ο admin
                        // μπορεί απλά να μεγαλώσει τα quota από το UI.
                        return fmt.Errorf("EnsureDomain %s failed (quota too low): msg=%s body=%s",
                                domain, msgJoined, string(body))
                }
                return fmt.Errorf("EnsureDomain %s failed: type=%s msg=%s body=%s",
                        domain, resp.Type, msgJoined, string(body))
        default:
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

        msgJoined := joinMsg(resp.Msg)

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


/////// mysql //////
// UpdatePasswordsFromShadow walks a backup tree (./backup/<cpuser>) and for each
// domain/<shadow> file updates mailcow.mailbox.password with the hashes from cPanel.
//
// backupRoot typically looks like:
//   ./backup/mycpuser/myip.gr/shadow
//   ./backup/mycpuser/myip.gr/chris/...
//
// If domainFilter is non-empty, only that domain is processed.
func UpdatePasswordsFromShadow(cfg *config.Config, backupRoot, domainFilter string) error {
	dsn := cfg.MailcowDSN()
	if dsn == "" {
		return fmt.Errorf("mailcow MySQL DSN not configured")
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("open mysql: %w", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		return fmt.Errorf("ping mysql: %w", err)
	}

	entries, err := os.ReadDir(backupRoot)
	if err != nil {
		return fmt.Errorf("read backup root %s: %w", backupRoot, err)
	}

	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		domain := ent.Name()
		if domainFilter != "" && domain != domainFilter {
			continue
		}

		domainDir := filepath.Join(backupRoot, domain)
		shadowPath := filepath.Join(domainDir, "shadow")

		f, err := os.Open(shadowPath)
		if err != nil {
			if os.IsNotExist(err) {
				// No shadow for this domain, skip quietly
				continue
			}
			log.Printf("mailcow: open shadow %s: %v", shadowPath, err)
			continue
		}

		log.Printf("mailcow: updating passwords for domain %s from %s", domain, shadowPath)

		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}

			parts := strings.Split(line, ":")
			if len(parts) < 2 {
				continue
			}
			localPart := parts[0]
			rawHash := parts[1]
			if rawHash == "" {
				continue
			}

			var scheme string
			switch {
			case strings.HasPrefix(rawHash, "$6$"):
				scheme = "{SHA512-CRYPT}"
			case strings.HasPrefix(rawHash, "$1$"):
				scheme = "{MD5-CRYPT}"
			default:
				log.Printf("mailcow: unsupported shadow hash for %s@%s: %s", localPart, domain, rawHash)
				continue
			}

			password := scheme + rawHash
			username := fmt.Sprintf("%s@%s", localPart, domain)

			res, err := db.Exec(`UPDATE mailbox SET password = ? WHERE username = ?`, password, username)
			if err != nil {
				log.Printf("mailcow: UPDATE mailbox.password for %s failed: %v", username, err)
				continue
			}

			affected, _ := res.RowsAffected()
			if affected == 0 {
				log.Printf("mailcow: no mailbox row for %s (skipping)", username)
				continue
			}

			log.Printf("mailcow: updated password for %s (scheme %s)", username, scheme)
		}

		if err := scanner.Err(); err != nil {
			log.Printf("mailcow: reading shadow %s: %v", shadowPath, err)
		}

		_ = f.Close()
	}

	return nil
}
