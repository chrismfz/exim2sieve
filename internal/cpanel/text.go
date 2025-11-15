package cpanel

import (
    "bufio"
    "os"
    "strings"

    "exim2sieve/internal/sieve"
)

// ParseFilterFile parses a cPanel-style Exim filter text file ("filter")
// into a sieve.Filter structure.
//
// Supports simple patterns like:
//
// #Name
// if
//  $header_from: contains "foo"
//  or $header_subject: begins "WHMCS"
// then
//  deliver "\"$local_part+Nixpal\"@$domain"
//  finish
// endif
//
// Anything more complex (nested ifs, error_message stuff) is ignored for now.
func ParseFilterFile(path string) (sieve.Filter, error) {
    f, err := os.Open(path)
    if err != nil {
        return sieve.Filter{}, err
    }
    defer f.Close()

    scanner := bufio.NewScanner(f)

    var entries []sieve.FilterEntry
    var curName string
    var condLines []string
    var actionLines []string
    inIf := false
    inThen := false

    flush := func() {
        if curName == "" || len(condLines) == 0 {
            curName = ""
            condLines = nil
            actionLines = nil
            inIf = false
            inThen = false
            return
        }

        rules := parseConditions(condLines)
        actions := parseActions(actionLines)

        if len(rules) == 0 || len(actions) == 0 {
            // nothing useful
            curName = ""
            condLines = nil
            actionLines = nil
            inIf = false
            inThen = false
            return
        }

        entries = append(entries, sieve.FilterEntry{
            Filtername: curName,
            Enabled:    1,
            Rules:      rules,
            Actions:    actions,
        })

        curName = ""
        condLines = nil
        actionLines = nil
        inIf = false
        inThen = false
    }

    for scanner.Scan() {
        line := strings.TrimSpace(scanner.Text())
        if line == "" {
            continue
        }

        // Skip boilerplate
        if strings.HasPrefix(line, "# Exim filter") ||
            strings.HasPrefix(line, "# Do not manually") ||
            strings.HasPrefix(line, "headers charset") ||
            strings.HasPrefix(line, "if not first_delivery") {
            continue
        }

        // New filter: "#Name"
        if strings.HasPrefix(line, "#") && !inIf && !inThen {
            flush()
            curName = strings.TrimSpace(strings.TrimPrefix(line, "#"))
            continue
        }

        // Start of IF
        if strings.HasPrefix(line, "if") && !inIf && !inThen {
            rest := strings.TrimSpace(strings.TrimPrefix(line, "if"))
            inIf = true
            if rest != "" {
                condLines = append(condLines, rest)
            }
            continue
        }

        if inIf {
            if strings.HasPrefix(line, "then") {
                inIf = false
                inThen = true
                continue
            }
            condLines = append(condLines, line)
            continue
        }

        if inThen {
            if strings.HasPrefix(line, "endif") {
                inThen = false
                flush()
                continue
            }
            actionLines = append(actionLines, line)
            continue
        }
    }

    if err := scanner.Err(); err != nil {
        return sieve.Filter{}, err
    }

    // Just in case file doesn't end with endif
    flush()

    return sieve.Filter{
        Filter:  entries,
        Version: "text",
    }, nil
}

// ───── Conditions parser ─────

func parseConditions(lines []string) []sieve.Rule {
    joined := strings.Join(lines, " ")
    joined = strings.TrimSpace(joined)
    if joined == "" {
        return nil
    }

    // We support simple patterns like:
    //
    // $header_from: contains "foo"
    // or $header_subject: begins "WHMCS"
    // and $header_from: contains "myip"
    //
    var rules []sieve.Rule

    s := joined
    first := true

    for {
        s = strings.TrimSpace(s)
        if s == "" {
            break
        }

        opt := "or"
        if !first {
            lower := strings.ToLower(s)
            switch {
            case strings.HasPrefix(lower, "or "):
                opt = "or"
                s = strings.TrimSpace(s[3:])
            case strings.HasPrefix(lower, "and "):
                opt = "and"
                s = strings.TrimSpace(s[4:])
            }
        }
        first = false

        if s == "" {
            break
        }

        // Find next " or " / " and " to isolate this condition
        lower := strings.ToLower(s)
        nextOr := strings.Index(lower, " or ")
        nextAnd := strings.Index(lower, " and ")
        cut := len(s)
        if nextOr >= 0 && nextOr < cut {
            cut = nextOr
        }
        if nextAnd >= 0 && nextAnd < cut {
            cut = nextAnd
        }
        expr := strings.TrimSpace(s[:cut])
        if cut < len(s) {
            s = s[cut:]
        } else {
            s = ""
        }

        if expr == "" {
            continue
        }

        // expr looks like: $header_from: contains "foo"
        colon := strings.Index(expr, ":")
        if colon < 0 {
            continue
        }
        part := strings.TrimSpace(expr[:colon+1])
        rest := strings.TrimSpace(expr[colon+1:])

        fields := strings.Fields(rest)
        if len(fields) == 0 {
            continue
        }
        match := strings.ToLower(fields[0])

        // Extract quoted value
        q1 := strings.Index(rest, "\"")
        if q1 < 0 {
            continue
        }
        vPart := rest[q1+1:]
        q2 := strings.Index(vPart, "\"")
        if q2 < 0 {
            continue
        }
        val := vPart[:q2]

        rules = append(rules, sieve.Rule{
            Part:  part,
            Match: match,
            Val:   val,
            Opt:   opt,
        })
    }

    return rules
}

// ───── Actions parser ─────

func parseActions(lines []string) []sieve.Action {
    var acts []sieve.Action

    for _, line := range lines {
        line = strings.TrimSpace(line)
        if line == "" {
            continue
        }
        lower := strings.ToLower(line)

        if strings.HasPrefix(lower, "finish") {
            acts = append(acts, sieve.Action{Action: "finish"})
            continue
        }

        if strings.HasPrefix(lower, "deliver ") {
            arg := extractFirstQuoted(line)
            if arg == "" {
                continue
            }

            // Special-case: deliver "\"$local_part+Nixpal\"@$domain"
            if strings.Contains(arg, "$local_part+") {
                idx := strings.Index(arg, "$local_part+")
                rest := arg[idx+len("$local_part+"):]
                name := rest
                if i := strings.IndexAny(rest, "\"@"); i >= 0 {
                    name = rest[:i]
                }
                name = strings.TrimSpace(name)
                if name != "" {
                    acts = append(acts, sieve.Action{
                        Action: "save",
                        Dest:   name, // mailbox name like "Nixpal"
                    })
                }
            } else {
                // deliver "logs@myip.gr"  → treat as redirect-like
                acts = append(acts, sieve.Action{
                    Action: "deliver",
                    Dest:   arg,
                })
            }
            continue
        }

        if strings.HasPrefix(lower, "save ") {
            arg := extractFirstQuoted(line)
            if arg == "" {
                continue
            }
            acts = append(acts, sieve.Action{
                Action: "save",
                Dest:   arg,
            })
            continue
        }

        // For now ignore anything else (nested ifs etc.)
    }

    return acts
}

func extractFirstQuoted(s string) string {
    start := strings.Index(s, "\"")
    if start < 0 {
        return ""
    }
    rest := s[start+1:]
    end := strings.Index(rest, "\"")
    if end < 0 {
        return ""
    }
    return rest[:end]
}
