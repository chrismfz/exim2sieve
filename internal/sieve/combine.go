package sieve

import (
    "fmt"
    "sort"
    "strings"
)

// CombineScripts merges many SieveScript objects into a single script:
//
// - Deduplicates all "require [...]" lines and moves them to the top.
// - Keeps all the IF blocks from each filter, separated by comments.
func CombineScripts(name string, scripts []SieveScript) SieveScript {
    reqSet := map[string]bool{}
    var bodyChunks []string

    for _, sc := range scripts {
        lines := strings.Split(sc.Content, "\n")
        var filtered []string

        for _, line := range lines {
            trimmed := strings.TrimSpace(line)

            if strings.HasPrefix(trimmed, "require [") {
                // Parse inside brackets: require ["fileinto", "reject"];
                start := strings.Index(trimmed, "[")
                end := strings.Index(trimmed, "]")
                if start != -1 && end != -1 && end > start {
                    inner := trimmed[start+1 : end]
                    parts := strings.Split(inner, ",")
                    for _, p := range parts {
                        m := strings.TrimSpace(p)
                        m = strings.Trim(m, `"`) // strip quotes
                        if m != "" {
                            reqSet[m] = true
                        }
                    }
                }
                continue // drop this require from the body
            }

            filtered = append(filtered, line)
        }

        // Skip completely empty bodies
        content := strings.TrimSpace(strings.Join(filtered, "\n"))
        if content == "" {
            continue
        }

        bodyChunks = append(bodyChunks, fmt.Sprintf("# Filter: %s", sc.Name))
        bodyChunks = append(bodyChunks, content)
        bodyChunks = append(bodyChunks, "") // blank line between filters
    }

    var b strings.Builder

    // Top-level require header
    if len(reqSet) > 0 {
        var mods []string
        for m := range reqSet {
            mods = append(mods, fmt.Sprintf("%q", m))
        }
        sort.Strings(mods)

        b.WriteString("require [")
        b.WriteString(strings.Join(mods, ", "))
        b.WriteString("];\n\n")
    }

    // All filter bodies one after another
    if len(bodyChunks) > 0 {
        b.WriteString(strings.Join(bodyChunks, "\n"))
    }

    return SieveScript{
        Name:    name,
        Content: b.String(),
    }
}
