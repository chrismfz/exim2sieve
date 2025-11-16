package cpanel

import (
    "fmt"
    "io"
    "io/fs"
    "os"
    "path/filepath"

    "exim2sieve/internal/sieve"
)

// ExportUser exports all filters for a cPanel user into destDir, with layout:
//
// destDir/user/domain/_domain.sieve
// destDir/user/domain/_domain.filter          (raw /etc/vfilters/domain, if exists)
// destDir/user/domain/localpart/localpart.sieve
// destDir/user/domain/localpart/filter        (raw text filter, if exists)
// destDir/user/domain/localpart/filter.yaml   (raw yaml filter, if exists)
// destDir/user/domain/localpart/maildir/...   (optional Maildir copy, if withMaildir=true)

func ExportUser(user, destDir string, withMaildir bool) error {
    homeDir, err := findHomeDir(user)
    if err != nil {
        return err
    }

    etcRoot := filepath.Join(homeDir, "etc")
    entries, err := os.ReadDir(etcRoot)
    if err != nil {
        return fmt.Errorf("reading %s: %w", etcRoot, err)
    }

    for _, e := range entries {
        if !e.IsDir() {
            continue
        }
        domain := e.Name()

        domainOutDir := filepath.Join(destDir, user, domain)
        if err := os.MkdirAll(domainOutDir, 0755); err != nil {
            return fmt.Errorf("mkdir %s: %w", domainOutDir, err)
        }

        // ── 1) Domain-wide filter: /etc/vfilters/<domain> ─────────────
        vfilterPath := filepath.Join("/etc/vfilters", domain)
        if fileExists(vfilterPath) {
            // Copy raw vfilter for backup
            _ = copyFile(vfilterPath, filepath.Join(domainOutDir, "_domain.filter"))

            // Parse + convert to sieve
            fDom, err := ParseFilterFile(vfilterPath)
            if err == nil {
                scripts := sieve.ConvertFilters(fDom)
                if len(scripts) > 0 {
                    combined := sieve.CombineScripts("_domain", scripts)
                    if err := sieve.WriteScripts([]sieve.SieveScript{combined}, domainOutDir); err != nil {
                        return fmt.Errorf("write domain sieve for %s: %w", domain, err)
                    }
                }
            }
        }

        // ── 2) Per-mailbox filters under /home*/user/etc/<domain>/localpart/ ──
        domainEtcDir := filepath.Join(etcRoot, domain)
        mboxEntries, err := os.ReadDir(domainEtcDir)
        if err != nil {
            // If etc/<domain> disappeared, skip
            continue
        }

        for _, me := range mboxEntries {
            if !me.IsDir() {
                continue
            }
            localpart := me.Name()

            mboxEtcDir := filepath.Join(domainEtcDir, localpart)
            mboxOutDir := filepath.Join(domainOutDir, localpart)
            if err := os.MkdirAll(mboxOutDir, 0755); err != nil {
                return fmt.Errorf("mkdir %s: %w", mboxOutDir, err)
            }

            yamlPath := filepath.Join(mboxEtcDir, "filter.yaml")
            textPath := filepath.Join(mboxEtcDir, "filter")

            var f sieve.Filter
            var haveFilter bool

            if fileExists(yamlPath) {
                // Backup original YAML
                _ = copyFile(yamlPath, filepath.Join(mboxOutDir, "filter.yaml"))

                data, err := os.ReadFile(yamlPath)
                if err != nil {
                    continue
                }
                if err := yamlUnmarshal(data, &f); err != nil {
                    continue
                }
                haveFilter = true
            } else if fileExists(textPath) {
                // Backup original text filter
                _ = copyFile(textPath, filepath.Join(mboxOutDir, "filter"))

                parsed, err := ParseFilterFile(textPath)
                if err != nil {
                    continue
                }
                f = parsed
                haveFilter = true
            }

            if !haveFilter {
                continue
            }


            // Optional: export Maildir for this mailbox
            if withMaildir {
                maildirSrc := filepath.Join(homeDir, "mail", domain, localpart)
                maildirDst := filepath.Join(mboxOutDir, "maildir")
                if dirExists(maildirSrc) {
                    if err := copyDir(maildirSrc, maildirDst); err != nil {
                        return fmt.Errorf("copy maildir for %s@%s: %w", localpart, domain, err)
                    }
                }
            }


            scripts := sieve.ConvertFilters(f)
            if len(scripts) == 0 {
                continue
            }

            combined := sieve.CombineScripts(localpart, scripts)
            if err := sieve.WriteScripts([]sieve.SieveScript{combined}, mboxOutDir); err != nil {
                return fmt.Errorf("write sieve for %s@%s: %w", localpart, domain, err)
            }
        }
    }

    return nil
}

// findHomeDir tries /home, /home2, /home3 for the cPanel user.
func findHomeDir(user string) (string, error) {
    candidates := []string{
        filepath.Join("/home", user),
        filepath.Join("/home2", user),
        filepath.Join("/home3", user),
    }
    for _, c := range candidates {
        if fi, err := os.Stat(c); err == nil && fi.IsDir() {
            return c, nil
        }
    }
    return "", fmt.Errorf("home directory for user %q not found in /home*/", user)
}

func fileExists(path string) bool {
    fi, err := os.Stat(path)
    return err == nil && !fi.IsDir()
}


func dirExists(path string) bool {
    fi, err := os.Stat(path)
    return err == nil && fi.IsDir()
}

// copyDir αντιγράφει αναδρομικά έναν φάκελο (Maildir) από src σε dst.
// - Δημιουργεί όλους τους ενδιάμεσους φακέλους.
// - Αντιγράφει μόνο regular files (αγνοεί symlinks κλπ).
func copyDir(src, dst string) error {
    return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
        if err != nil {
            return err
        }

        rel, err := filepath.Rel(src, path)
        if err != nil {
            return err
        }
        target := filepath.Join(dst, rel)

        if d.IsDir() {
            // Create directory in destination
            return os.MkdirAll(target, 0755)
        }

        if !d.Type().IsRegular() {
            return nil // skip non-regular files (symlinks, sockets, etc.)
        }
        return copyFile(path, target)
    })
}


func copyFile(src, dst string) error {
    in, err := os.Open(src)
    if err != nil {
        return err
    }
    defer in.Close()

    out, err := os.Create(dst)
    if err != nil {
        return err
    }
    defer out.Close()

    if _, err := io.Copy(out, in); err != nil {
        return err
    }
    return out.Sync()
}

// yamlUnmarshal is a tiny wrapper so we don't import yaml here directly
// (to avoid cycles, keep it abstract). We implement it in a separate file
// in this package.
