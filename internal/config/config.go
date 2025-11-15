package config

import (
    "bufio"
    "fmt"
    "os"
    "path/filepath"
    "strings"
)

type Config struct {
    DoveadmCmd []string // e.g. {"doveadm"} or {"docker", "exec", "-i", "...", "doveadm"}
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
        DoveadmCmd: []string{"doveadm"},
    }, nil
}

func loadFrom(path string) (*Config, error) {
    f, err := os.Open(path)
    if err != nil {
        return nil, err
    }
    defer f.Close()

    scanner := bufio.NewScanner(f)
    inDoveadm := false
    var cmdLine string

    for scanner.Scan() {
        line := strings.TrimSpace(scanner.Text())
        if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
            continue
        }

        // section headers: [doveadm]
        if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
            section := strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
            inDoveadm = (section == "doveadm")
            continue
        }

        if !inDoveadm {
            continue
        }

        // key = value
        if idx := strings.Index(line, "="); idx != -1 {
            key := strings.ToLower(strings.TrimSpace(line[:idx]))
            val := strings.TrimSpace(line[idx+1:])
            if key == "command" {
                cmdLine = val
                break
            }
        }
    }

    if err := scanner.Err(); err != nil {
        return nil, fmt.Errorf("reading config %s: %w", filepath.Base(path), err)
    }

    if cmdLine == "" {
        return nil, fmt.Errorf("no [doveadm].command found in %s", path)
    }

    parts := strings.Fields(cmdLine)
    if len(parts) == 0 {
        return nil, fmt.Errorf("empty doveadm command in %s", path)
    }

    return &Config{
        DoveadmCmd: parts,
    }, nil
}
