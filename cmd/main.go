package main

import (
    "bytes"
    "bufio"
    "flag"
    "fmt"
    "io/ioutil"
    "log"
    "os"
    "strings"

    "gopkg.in/yaml.v3"

    "exim2sieve/internal/cpanel"
    "exim2sieve/internal/sieve"
    "exim2sieve/internal/config"
    "exim2sieve/internal/importer"
)

func main() {
    account := flag.String("account", "", "Alias for -cpanel-user (cPanel account)")
    dest := flag.String("dest", "./backup", "Destination folder for sieve scripts")
    path := flag.String("path", "", "Convert a single filter.yaml or filter file")
    cpUser := flag.String("cpanel-user", "", "Export filters for a cPanel account (domains + mailboxes)")

    // Import-related flags
    importSieve := flag.Bool("import-sieve", false, "Import Sieve scripts from a backup using doveadm")
    backupRoot := flag.String("backup", "", "Backup root for -import-sieve (e.g. ./backup/myipgr)")
    domain := flag.String("domain", "", "Limit -import-sieve to a specific domain (optional)")
    configPath := flag.String("config", "", "Path to exim2sieve.conf (optional)")

    flag.Parse()

    // Make -account act as a shortcut for -cpanel-user
    if *cpUser == "" && *account != "" {
        *cpUser = *account
    }

    // Decide mode
    modeExportUser := (*cpUser != "")
    modeSingleFile := (*path != "")
    modeImport := *importSieve

    // If no mode flags are provided, show help and exit.
    if !modeExportUser && !modeSingleFile && !modeImport {
        fmt.Fprintf(os.Stderr, "exim2sieve – convert cPanel Exim filters to Sieve\n\n")
        fmt.Fprintf(os.Stderr, "Usage:\n")
        fmt.Fprintf(os.Stderr, "  %s [flags]\n\n", os.Args[0])
        fmt.Fprintf(os.Stderr, "Modes (choose one):\n")
        fmt.Fprintf(os.Stderr, "  -cpanel-user <user>   Export all filters for a cPanel account\n")
        fmt.Fprintf(os.Stderr, "  -account <user>       Alias for -cpanel-user (same as above)\n")
        fmt.Fprintf(os.Stderr, "  -path <file>          Convert a single filter.yaml or filter file\n")
        fmt.Fprintf(os.Stderr, "  -import-sieve         Import Sieve scripts from a backup using doveadm\n\n")

        fmt.Fprintf(os.Stderr, "Export example:\n")
        fmt.Fprintf(os.Stderr, "./exim2sieve -cpanel-user myipgr -dest ./backup\n")
        fmt.Fprintf(os.Stderr, "Import example:\n")
        fmt.Fprintf(os.Stderr, "./exim2sieve -config exim2sieve.conf  -import-sieve -backup ./backup/myipgr -domain myip.gr \n")



        fmt.Fprintf(os.Stderr, "Other flags:\n")
        flag.PrintDefaults()
        os.Exit(1)
    }

    // Ensure modes are not mixed
    activeModes := 0
    if modeExportUser {
        activeModes++
    }
    if modeSingleFile {
        activeModes++
    }
    if modeImport {
        activeModes++
    }
    if activeModes > 1 {
        log.Fatal("Only one of -cpanel-user/-account, -path, or -import-sieve can be used at a time")
    }

    // 0️⃣ Import mode: use doveadm to load Sieve into Dovecot
    if modeImport {
        if *backupRoot == "" {
            log.Fatal("-backup is required with -import-sieve")
        }
        cfg, err := config.Load(*configPath)
        if err != nil {
            log.Fatalf("Cannot load config: %v", err)
        }

        ic := importer.ImportConfig{
            BackupRoot: *backupRoot,
            Domain:     *domain,
            DoveadmCmd: cfg.DoveadmCmd,
        }

        if err := importer.ImportSieve(ic); err != nil {
            log.Fatal(err)
        }
        return
    }

    // 1️⃣ Full cPanel user export (domains + mailboxes)
    if modeExportUser {
        if modeSingleFile {
            log.Fatal("-cpanel-user/-account cannot be combined with -path")
        }
        if err := cpanel.ExportUser(*cpUser, *dest); err != nil {
            log.Fatal(err)
        }
        return
    }

    // 2️⃣ Single file mode: demo / standalone
    if modeSingleFile {
        handleSingleFile(*path, *dest)
        return
    }

    // Should never get here
    log.Fatal("No valid mode selected (this should be unreachable)")
}

func handleSingleFile(path string, dest string) {
    data, err := ioutil.ReadFile(path)
    if err != nil {
        log.Fatalf("Cannot read file: %v\n", err)
    }

    // Decide if this is YAML (filter.yaml) or text Exim filter ("filter")
    if isYAML(data) {
        var f sieve.Filter
        if err := yaml.Unmarshal(data, &f); err != nil {
            log.Fatalf("YAML parse error: %v\n", err)
        }
        scripts := sieve.ConvertFilters(f)
        if len(scripts) == 0 {
            log.Println("No enabled filters in YAML, nothing to export.")
            return
        }
        combined := sieve.CombineScripts("filters", scripts)

        if err := sieve.WriteScripts([]sieve.SieveScript{combined}, dest); err != nil {
            log.Fatalf("Cannot write sieve scripts: %v\n", err)
        }

        fmt.Printf(
            "Exported %d filters into %s/filters.sieve (YAML)\n",
            len(scripts), dest,
        )
        return
    }

    // Text Exim filter mode
    f, err := cpanel.ParseFilterFile(path)
    if err != nil {
        log.Fatalf("Cannot parse Exim filter: %v\n", err)
    }

    scripts := sieve.ConvertFilters(f)
    if len(scripts) == 0 {
        log.Println("No enabled filters, nothing to export.")
        return
    }

    // Single-file mode: also produce one combined filters.sieve
    combined := sieve.CombineScripts("filters", scripts)

    if err := sieve.WriteScripts([]sieve.SieveScript{combined}, dest); err != nil {
        log.Fatalf("Cannot write sieve scripts: %v\n", err)
    }

    fmt.Printf(
        "Exported %d filters into %s/filters.sieve\n",
        len(scripts), dest,
    )
}

// isYAML does a cheap detection whether the file looks like a cPanel YAML filter
// (filter.yaml) instead of a plain Exim text filter ("filter").
func isYAML(data []byte) bool {
    scanner := bufio.NewScanner(bytes.NewReader(data))
    for scanner.Scan() {
        line := strings.TrimSpace(scanner.Text())
        if line == "" || strings.HasPrefix(line, "#") {
            continue
        }
        if strings.HasPrefix(line, "---") ||
            strings.HasPrefix(line, "version:") ||
            strings.HasPrefix(line, "filter:") {
            return true
        }
        break
    }
    return false
}
