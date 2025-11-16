package importer

import (
    "bytes"
    "fmt"
    "log"
    "os"
    "os/exec"
    "path/filepath"
    "strings"
)

// ImportMaildir walks the backup tree and imports Maildir contents using doveadm import.
func ImportMaildir(cfg ImportConfig) error {
    if cfg.BackupRoot == "" {
        return fmt.Errorf("ImportMaildir: BackupRoot is empty")
    }
    if len(cfg.DoveadmCmd) == 0 {
        return fmt.Errorf("ImportMaildir: DoveadmCmd is empty")
    }

    info, err := os.Stat(cfg.BackupRoot)
    if err != nil {
        return fmt.Errorf("stat BackupRoot: %w", err)
    }
    if !info.IsDir() {
        return fmt.Errorf("BackupRoot %s is not a directory", cfg.BackupRoot)
    }

    domains, err := os.ReadDir(cfg.BackupRoot)
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
        users, err := os.ReadDir(domainPath)
        if err != nil {
            log.Printf("WARN: cannot read domain dir %s: %v", domainPath, err)
            continue
        }

        for _, u := range users {
            if !u.IsDir() {
                continue
            }
            uname := u.Name()
            if strings.HasPrefix(uname, "@") || strings.HasPrefix(uname, "_") {
                continue
            }

            userDir := filepath.Join(domainPath, uname)
            addr := uname + "@" + domain

            // Optional mailbox filter
            if cfg.Mailbox != "" {
                if cfg.Mailbox != addr && cfg.Mailbox != uname {
                    continue
                }
            }

            maildirPath := filepath.Join(userDir, "maildir")
            ok, err := isDir(maildirPath)
            if err != nil {
                log.Printf("WARN: stat maildir for %s: %v", addr, err)
                skipped++
                continue
            }
            if !ok {
                log.Printf("INFO: no maildir for %s (expected %s)", addr, maildirPath)
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
                log.Printf("WARN: mailbox %s not found on target, skipping maildir %s", addr, maildirPath)
                skipped++
                continue
            }

            if err := doveadmImportMaildir(cfg, addr, maildirPath); err != nil {
                log.Printf("ERROR: importing maildir for %s from %s: %v", addr, maildirPath, err)
                skipped++
                continue
            }

            log.Printf("Imported maildir for %s from %s", addr, maildirPath)
            imported++
        }
    }

    log.Printf("Maildir import completed: imported=%d, skipped=%d", imported, skipped)
    return nil
}

func isDir(path string) (bool, error) {
    fi, err := os.Stat(path)
    if err != nil {
        if os.IsNotExist(err) {
            return false, nil
        }
        return false, err
    }
    return fi.IsDir(), nil
}


// mapMaildirPathForDoveadm χαρτογραφεί host path -> container path αν χρειάζεται.
// - Αν MaildirHostBase/ContainerBase είναι κενά, επιστρέφει το host path.
// - Αν το host path ξεκινάει με MaildirHostBase, το αντικαθιστά με MaildirContainerBase.
func mapMaildirPathForDoveadm(cfg ImportConfig, hostPath string) string {
    hostBase := strings.TrimRight(cfg.MaildirHostBase, "/")
    contBase := strings.TrimRight(cfg.MaildirContainerBase, "/")

    if hostBase == "" || contBase == "" {
        // Non-docker / direct mode: use host path directly
        return "maildir:" + hostPath
    }

    // Αν το hostPath είναι κάτω από το hostBase → κάνουμε σχετικό & remap
    if hostPath == hostBase || strings.HasPrefix(hostPath, hostBase+"/") {
        rel, err := filepath.Rel(hostBase, hostPath)
        if err == nil {
            mapped := filepath.Join(contBase, rel)
            return "maildir:" + mapped
        }
    }

    // Fallback: δεν ταιριάζει με hostBase, χρησιμοποίησε το as-is
    return "maildir:" + hostPath
}


// doveadmImportMaildir runs:
//   doveadm import -u <addr> maildir:/path "" ALL
//

func doveadmImportMaildir(cfg ImportConfig, addr, maildirPath string) error {
    // Basic sanity: ensure maildirPath "looks like" Maildir (cur/new)
    hasCur := false
    hasNew := false
    entries, _ := os.ReadDir(maildirPath)
    for _, e := range entries {
        if e.IsDir() && e.Name() == "cur" {
            hasCur = true
        }
        if e.IsDir() && e.Name() == "new" {
            hasNew = true
        }
    }
    if !hasCur && !hasNew {
        log.Printf("WARN: %s doesn't look like a Maildir (no cur/ or new/)", maildirPath)
    }

    loc := mapMaildirPathForDoveadm(cfg, maildirPath)

    args := append(cfg.DoveadmCmd[1:], "import", "-u", addr, loc, "", "ALL")
    cmd := exec.Command(cfg.DoveadmCmd[0], args...)
    var out bytes.Buffer
    cmd.Stderr = &out

    if err := cmd.Run(); err != nil {
        return fmt.Errorf("doveadm import failed: %v, stderr=%s", err, out.String())
    }
    return nil
}
