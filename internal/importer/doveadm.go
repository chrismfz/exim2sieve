package importer

import (
    "bytes"
    "fmt"
    "io/ioutil"
    "log"
    "os"
    "os/exec"
    "path/filepath"
    "strings"
)

// ImportConfig describes how to import Sieve scripts from a backup.
type ImportConfig struct {
    BackupRoot string   // e.g. "./backup/myipgr"
    Domain     string   // optional: only this domain (e.g. "myip.gr")
    Mailbox    string   // optional: only this mailbox (localpart or full addr)
    DoveadmCmd []string // e.g. {"doveadm"} or {"docker","exec","-i","ctr","doveadm"}

    // Optional Maildir path mapping (mainly for ImportMaildir, but kept here
    // for a unified config object).
    MaildirHostBase, MaildirContainerBase string

}

// ImportSieve walks the backup tree and imports Sieve scripts using doveadm.
// It logs per-user errors but keeps going; returns an error only for fatal
// issues (e.g. invalid BackupRoot).
func ImportSieve(cfg ImportConfig) error {
    if cfg.BackupRoot == "" {
        return fmt.Errorf("ImportSieve: BackupRoot is empty")
    }
    if len(cfg.DoveadmCmd) == 0 {
        return fmt.Errorf("ImportSieve: DoveadmCmd is empty")
    }

    info, err := os.Stat(cfg.BackupRoot)
    if err != nil {
        return fmt.Errorf("stat BackupRoot: %w", err)
    }
    if !info.IsDir() {
        return fmt.Errorf("BackupRoot %s is not a directory", cfg.BackupRoot)
    }

    domains, err := ioutil.ReadDir(cfg.BackupRoot)
    if err != nil {
        return fmt.Errorf("read BackupRoot: %w", err)
    }

    imported := 0
    skipped := 0

    for _, d := range domains {
        if !d.IsDir() {
            continue
        }
        domain := d.Name()
        if cfg.Domain != "" && cfg.Domain != domain {
            continue
        }

        domainPath := filepath.Join(cfg.BackupRoot, domain)
        users, err := ioutil.ReadDir(domainPath)
        if err != nil {
            log.Printf("WARN: cannot read domain dir %s: %v", domainPath, err)
            continue
        }

        for _, u := range users {
            // Skip non-dirs and special dirs like @pwcache, _domain.filter
            if !u.IsDir() {
                continue
            }
            uname := u.Name()
            if strings.HasPrefix(uname, "@") || strings.HasPrefix(uname, "_") {
                continue
            }

            userDir := filepath.Join(domainPath, uname)
            addr := uname + "@" + domain

            // Αν έχεις δώσει -mailbox, φιλτράρουμε εδώ:
            //   -mailbox chris        => ταιριάζει uname == "chris"
            //   -mailbox chris@myip.gr => ταιριάζει addr == "chris@myip.gr"
            if cfg.Mailbox != "" && cfg.Mailbox != addr && cfg.Mailbox != uname {
                continue
            }

            // Find a .sieve file inside userDir (prefer <user>.sieve)
            sievePath, scriptName, err := findSieveFile(userDir, uname)

            if err != nil {
                log.Printf("INFO: no Sieve script for %s: %v", addr, err)
                skipped++
                continue
            }


            exists, err := doveadmUserExists(cfg.DoveadmCmd, addr)
            if err != nil {
                log.Printf("ERROR: doveadm user check for %s: %v", addr, err)
                skipped++
                continue
            }
            if !exists {
                log.Printf("WARN: mailbox %s not found on target, skipping %s", addr, sievePath)
                skipped++
                continue
            }

            if err := doveadmPut(cfg.DoveadmCmd, addr, scriptName, sievePath); err != nil {
                log.Printf("ERROR: importing sieve for %s from %s: %v", addr, sievePath, err)
                skipped++
                continue
            }

            if err := doveadmActivate(cfg.DoveadmCmd, addr, scriptName); err != nil {
                log.Printf("WARN: could not activate sieve %s for %s: %v", scriptName, addr, err)
                // not fatal for the overall import
            }

            log.Printf("Imported sieve for %s from %s as %s", addr, sievePath, scriptName)
            imported++
        }
    }

    log.Printf("Import completed: imported=%d, skipped=%d", imported, skipped)
    return nil
}

func findSieveFile(userDir, uname string) (path string, scriptName string, err error) {
    entries, err := ioutil.ReadDir(userDir)
    if err != nil {
        return "", "", err
    }

    // Prefer "<user>.sieve"
    preferred := uname + ".sieve"
    for _, e := range entries {
        if !e.IsDir() && e.Name() == preferred {
            return filepath.Join(userDir, e.Name()), "cpanel-migrated", nil
        }
    }

    // Otherwise pick the first *.sieve file
    for _, e := range entries {
        if !e.IsDir() && strings.HasSuffix(e.Name(), ".sieve") {
            return filepath.Join(userDir, e.Name()), "cpanel-migrated", nil
        }
    }

    return "", "", fmt.Errorf("no .sieve file found in %s", userDir)
}

func doveadmUserExists(doveadmCmd []string, addr string) (bool, error) {
    args := append(doveadmCmd[1:], "user", "-u", addr)
    cmd := exec.Command(doveadmCmd[0], args...)
    if err := cmd.Run(); err != nil {
        // ExitError means "user not found" or similar
        if _, ok := err.(*exec.ExitError); ok {
            return false, nil
        }
        return false, err
    }
    return true, nil
}

func doveadmPut(doveadmCmd []string, addr, scriptName, sievePath string) error {
    data, err := ioutil.ReadFile(sievePath)
    if err != nil {
        return fmt.Errorf("read sieve file: %w", err)
    }

    args := append(doveadmCmd[1:], "sieve", "put", "-u", addr, scriptName)
    cmd := exec.Command(doveadmCmd[0], args...)
    cmd.Stdin = bytes.NewReader(data)
    var out bytes.Buffer
    cmd.Stderr = &out

    if err := cmd.Run(); err != nil {
        return fmt.Errorf("doveadm sieve put failed: %v, stderr=%s", err, out.String())
    }
    return nil
}

func doveadmActivate(doveadmCmd []string, addr, scriptName string) error {
    args := append(doveadmCmd[1:], "sieve", "activate", "-u", addr, scriptName)
    cmd := exec.Command(doveadmCmd[0], args...)
    var out bytes.Buffer
    cmd.Stderr = &out

    if err := cmd.Run(); err != nil {
        return fmt.Errorf("doveadm sieve activate failed: %v, stderr=%s", err, out.String())
    }
    return nil
}
