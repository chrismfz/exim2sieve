package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Filter struct {
	Filter []struct {
		Filtername string `yaml:"filtername"`
		Enabled    int    `yaml:"enabled"`
		Rules      []struct {
			Part  string `yaml:"part"`
			Match string `yaml:"match"`
			Val   string `yaml:"val"`
			Opt   string `yaml:"opt"`
		} `yaml:"rules"`
		Actions []struct {
			Action string `yaml:"action"`
			Dest   string `yaml:"dest"`
		} `yaml:"actions"`
	} `yaml:"filter"`
	Version string `yaml:"version"`
}


func main() {
    all := flag.Bool("all", false, "Scan all users")
    account := flag.String("account", "", "Scan a specific account")
    dest := flag.String("dest", "./backup", "Destination folder for sieve scripts")
    path := flag.String("path", "", "Load a single filter.yaml file (no cPanel environment required)")

    flag.Parse()

    // 1️⃣ Αν δώθηκε --path, κάνουμε άμεσο import
    if *path != "" {
        handleSingleFile(*path, *dest)
        return
    }

    // 2️⃣ Χρειάζεται οπωσδήποτε --all ή --account
    if !*all && *account == "" {
        log.Fatal("You must specify --all or --account <name>")
    }

    var users []string
    homeDir := "/home"

    if *all {
        files, err := ioutil.ReadDir(homeDir)
        if err != nil {
            log.Fatal(err)
        }
        for _, f := range files {
            if f.IsDir() {
                users = append(users, f.Name())
            }
        }
    } else if *account != "" {
        users = append(users, *account)
    }

    for _, u := range users {
        filterPath := filepath.Join(homeDir, u, "etc", u, "filter.yaml")
        if _, err := os.Stat(filterPath); os.IsNotExist(err) {
            fmt.Printf("No filter.yaml for user %s\n", u)
            continue
        }

        data, err := ioutil.ReadFile(filterPath)
        if err != nil {
            log.Println("Error reading filter.yaml:", err)
            continue
        }

        var f Filter
        if err := yaml.Unmarshal(data, &f); err != nil {
            log.Println("YAML parse error:", err)
            continue
        }

        // dump basic sieve
        userDest := filepath.Join(*dest, u)
        if err := os.MkdirAll(userDest, 0755); err != nil {
            log.Println("Cannot create dest folder:", err)
            continue
        }

        for _, rule := range f.Filter {
            if rule.Enabled == 0 {
                continue
            }
            sieveFile := filepath.Join(userDest, fmt.Sprintf("%s.sieve", rule.Filtername))
            content := fmt.Sprintf("// sieve placeholder for filter %s\n", rule.Filtername)
            err := ioutil.WriteFile(sieveFile, []byte(content), 0644)
            if err != nil {
                log.Println("Cannot write sieve file:", err)
            }
        }
        fmt.Printf("Exported filters for user %s\n", u)
    }
}



func handleSingleFile(path string, dest string) {
    data, err := ioutil.ReadFile(path)
    if err != nil {
        log.Fatalf("Cannot read file: %v\n", err)
    }

    var f Filter
    if err := yaml.Unmarshal(data, &f); err != nil {
        log.Fatalf("YAML parse error: %v\n", err)
    }

    if err := os.MkdirAll(dest, 0755); err != nil {
        log.Fatalf("Cannot create dest folder: %v\n", err)
    }

    for _, rule := range f.Filter {
        if rule.Enabled == 0 {
            continue
        }
        out := filepath.Join(dest, fmt.Sprintf("%s.sieve", rule.Filtername))
        content := fmt.Sprintf("// sieve placeholder for filter %s\n", rule.Filtername)
        if err := ioutil.WriteFile(out, []byte(content), 0644); err != nil {
            log.Fatalf("Cannot write sieve file: %v\n", err)
        }
    }

    fmt.Printf("Exported %d filters to %s\n", len(f.Filter), dest)
}
