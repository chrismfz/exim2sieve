package config

import (
    "bufio"
    "fmt"
    "os"
    "strings"
    "strconv"

)

type Config struct {

    DoveadmCmd []string // e.g. {"doveadm"} or {"docker", "exec", "-i", "...", "doveadm"}

    // Mailcow API integration (optional, used by mailcow-related modes)
    MailcowAPIURL  string
    MailcowAPIKey  string
    MailcowQuotaMB int // default quota per mailbox in MB

    // Maildir path mapping (optional, mainly for Docker):
    // - On bare metal / DirectAdmin: leave empty → host paths used as-is.
    // - On Docker (mailcow): set host base + container base for Maildir backups.
    MaildirHostBase      string
    MaildirContainerBase string

}

// Load tries an explicit path (if given), then ./exim2sieve.conf, then
// /etc/exim2sieve.conf. If nothing is found, it falls back to a default
// config with DoveadmCmd=["doveadm"].
func Load(path string) (*Config, error) {
    if path != "" {
        cfg, err := loadFrom(path)
        if err != nil {
            return nil, err
        }
        return cfg, nil
    }

    // default search locations
    candidates := []string{
        "./exim2sieve.conf",
        "/etc/exim2sieve.conf",
    }

    for _, p := range candidates {
        cfg, err := loadFrom(p)

        if err == nil {
            return cfg, nil
        }
        if os.IsNotExist(err) {
            continue
        }
        // For other errors (permission, parse, etc.) return immediately.
        return nil, err


    }

    // Fallback: assume plain "doveadm" in PATH.
    return &Config{
        DoveadmCmd:         []string{"doveadm"},
        MailcowQuotaMB:     5120, // 5GB default
        MaildirHostBase:      "",
        MaildirContainerBase: "",
    }, nil
}

func loadFrom(path string) (*Config, error) {
    f, err := os.Open(path)
    if err != nil {
        return nil, err
    }
    defer f.Close()

    scanner := bufio.NewScanner(f)
    cfg := &Config{
        DoveadmCmd:         []string{"doveadm"},
        MailcowQuotaMB:     5120, // sensible default
        MaildirHostBase:      "",
        MaildirContainerBase: "",
    }

    currentSection := ""

    for scanner.Scan() {
        line := strings.TrimSpace(scanner.Text())
        if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
            continue
        }

        // section headers: [doveadm], [mailcow]
        if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
            currentSection = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
            continue
        }



        // key = value
        idx := strings.Index(line, "=")
        if idx == -1 {
            continue
        }
        key := strings.ToLower(strings.TrimSpace(line[:idx]))
        val := strings.TrimSpace(line[idx+1:])

        switch currentSection {
        case "doveadm":
            if key == "command" && val != "" {
                parts := strings.Fields(val)
                if len(parts) > 0 {
                    cfg.DoveadmCmd = parts
                }
            }
        case "mailcow":
            switch key {
            case "api_url":
                cfg.MailcowAPIURL = strings.TrimRight(val, "/")
            case "api_key":
                cfg.MailcowAPIKey = val
            case "default_quota":
                if mb, err := parseQuotaMB(val); err == nil {
                    cfg.MailcowQuotaMB = mb
                }


            }
        case "paths":
            switch key {
            case "maildir_host_base":
                // e.g. /root/chris/backup
                cfg.MaildirHostBase = strings.TrimRight(val, "/")
            case "maildir_container_base":
                // e.g. /backup (inside docker container)
                cfg.MaildirContainerBase = strings.TrimRight(val, "/")


            }
        }
    }

    if err := scanner.Err(); err != nil {
        return nil, fmt.Errorf("reading config %s: %w", path, err)
    }

    // No strict requirement for [doveadm] or [mailcow] – defaults are fine.
    return cfg, nil
}

// parseQuotaMB parses things like:
//   "3072", "5GB", "10G", "500MB", "500M"
// and returns MB (which is what mailcow wants for quota).
func parseQuotaMB(val string) (int, error) {
    s := strings.ToUpper(strings.TrimSpace(val))
    if s == "" {
        return 0, fmt.Errorf("empty quota")
    }

    mult := 1
    switch {
    case strings.HasSuffix(s, "GB"):
        mult = 1024
        s = strings.TrimSuffix(s, "GB")
    case strings.HasSuffix(s, "G"):
        mult = 1024
        s = strings.TrimSuffix(s, "G")
    case strings.HasSuffix(s, "MB"):
        mult = 1
        s = strings.TrimSuffix(s, "MB")
    case strings.HasSuffix(s, "M"):
        mult = 1
        s = strings.TrimSuffix(s, "M")
    }

    s = strings.TrimSpace(s)
    n, err := strconv.Atoi(s)
    if err != nil {
        return 0, fmt.Errorf("parseQuotaMB(%q): %w", val, err)
    }
    return n * mult, nil
}
