package sieve

import (
    "fmt"
    "sort"
    "strings"
)

// SieveScript represents a single output sieve file
type SieveScript struct {
    Name    string
    Content string
}

// ConvertFilters converts cPanel/Exim YAML filters into Sieve scripts.
func ConvertFilters(f Filter) []SieveScript {
    var scripts []SieveScript

    for _, flt := range f.Filter {
        if flt.Enabled == 0 {
            continue
        }

        var sb strings.Builder
        usedExt := map[string]bool{}
        usesBody := false

        // ── Build combined condition from all rules ────────────────────────
        if len(flt.Rules) == 0 {
            sb.WriteString("// Filter has no rules; nothing to match.\n")
            scripts = append(scripts, SieveScript{
                Name:    flt.Filtername,
                Content: sb.String(),
            })
            continue
        }

        cond, condUsesBody := buildConditions(flt.Rules)
        if condUsesBody {
            usesBody = true
        }

        // ── Determine required Sieve extensions from actions & body ──────
        if usesBody {
            usedExt["body"] = true
        }
        for _, a := range flt.Actions {
            switch strings.ToLower(strings.TrimSpace(a.Action)) {
            case "save", "deliver":
                usedExt["fileinto"] = true
            case "reject":
                usedExt["reject"] = true
            }
        }

        // ── require [...] header ──────────────────────────────────────────
        if len(usedExt) > 0 {
            var reqs []string
            for k := range usedExt {
                reqs = append(reqs, fmt.Sprintf("%q", k))
            }
            sort.Strings(reqs)
            sb.WriteString("require [")
            sb.WriteString(strings.Join(reqs, ", "))
            sb.WriteString("];\n\n")
        }

        // ── IF block ───────────────────────────────────────────────────────
        sb.WriteString("if ")
        sb.WriteString(cond)
        sb.WriteString(" {\n")

        // ── Actions ────────────────────────────────────────────────────────
        if len(flt.Actions) == 0 {
            sb.WriteString("    # TODO: no actions defined in original filter\n")
        } else {
            for _, a := range flt.Actions {
                action := strings.ToLower(strings.TrimSpace(a.Action))
                dest := a.Dest

                switch action {
                case "save":
                    mailbox := mailboxFromDest(dest)
                    sb.WriteString(fmt.Sprintf("    fileinto %s;\n", quoteString(mailbox)))
                    sb.WriteString(fmt.Sprintf("    # original path: %s\n", quoteString(dest)))
                case "deliver":
                    sb.WriteString(fmt.Sprintf("    fileinto %s;\n", quoteString(dest)))
                case "reject":
                    sb.WriteString(fmt.Sprintf("    reject %s;\n", quoteString(dest)))
                case "finish":
                    sb.WriteString("    # finish (Exim): terminate filter processing (handled by stop)\n")
                default:
                    sb.WriteString(fmt.Sprintf(
                        "    # TODO: unsupported action %q dest=%q\n",
                        a.Action, a.Dest,
                    ))
                }
            }
        }

        sb.WriteString("    stop;\n")
        sb.WriteString("}\n")

        scripts = append(scripts, SieveScript{
            Name:    flt.Filtername,
            Content: sb.String(),
        })
    }

    return scripts
}

// buildConditions builds a combined condition for a list of rules.
// Returns (expression, usesBodyTest).
func buildConditions(rules []Rule) (string, bool) {
    if len(rules) == 1 {
        return buildSingleCondition(&rules[0])
    }

    var conds []string
    usesBody := false
    hasAnd := false
    hasOr := false

    for i := range rules {
        r := &rules[i]

        opt := strings.ToLower(strings.TrimSpace(r.Opt))
        switch opt {
        case "and":
            hasAnd = true
        case "or", "":
            hasOr = true
        }

        c, cUsesBody := buildSingleCondition(r)
        conds = append(conds, c)
        if cUsesBody {
            usesBody = true
        }
    }

    join := "anyof"
    if hasAnd && !hasOr {
        join = "allof"
    }

    if len(conds) == 1 {
        return conds[0], usesBody
    }

    // Pretty-print:
    // anyof (
    //     cond1,
    //     cond2,
    // )
    var b strings.Builder
    b.WriteString(join)
    b.WriteString(" (\n")
    for i, c := range conds {
        b.WriteString("    ")
        b.WriteString(c)
        if i < len(conds)-1 {
            b.WriteString(",")
        }
        b.WriteString("\n")
    }
    b.WriteString(")")

    return b.String(), usesBody
}

// buildSingleCondition converts a single rule to a Sieve boolean expression.
// Returns (condition, usesBodyTest).
func buildSingleCondition(r *Rule) (string, bool) {
    part := strings.ToLower(strings.TrimSpace(r.Part))
    match := strings.ToLower(strings.TrimSpace(r.Match))
    val := r.Val

    // Regex-based matches are considered unsafe/unsupported — we drop them.
    if match == "matches_regex" || match == "does not match" {
        return fmt.Sprintf(
            "true /* TODO: regex/does-not-match rule ignored (%s %q %s) */",
            r.Part, r.Match, r.Val,
        ), false
    }

    field := mapPart(part)
    op, negative, bodyPattern := mapMatch(match, val)

    if op == "" && bodyPattern == "" {
        return fmt.Sprintf(
            "true /* TODO: unsupported match %q on %s %q */",
            r.Match, r.Part, r.Val,
        ), false
    }

    // Body: "body :contains \"...\"" etc.
    if field.kind == fieldBody {
        cond := fmt.Sprintf("body %s %s", op, quoteString(bodyPattern))
        if negative {
            cond = "not (" + cond + ")"
        }
        return cond, true
    }

    // Header/address fields
    hdrExpr := field.headerExpr()
    cond := fmt.Sprintf("%s %s %s %s", field.test(), op, hdrExpr, quoteString(bodyPattern))

    if negative {
        cond = "not (" + cond + ")"
    }

    return cond, false
}

// ─────────────────────────── Field mapping helpers ─────────────────────────

type fieldKind int

const (
    fieldHeader fieldKind = iota
    fieldAddress
    fieldBody
)

type fieldInfo struct {
    kind    fieldKind
    headers []string
}

func (f fieldInfo) test() string {
    switch f.kind {
    case fieldAddress:
        return "address"
    case fieldHeader:
        return "header"
    default:
        return "header"
    }
}

func (f fieldInfo) headerExpr() string {
    if len(f.headers) == 1 {
        return quoteString(f.headers[0])
    }
    var parts []string
    for _, h := range f.headers {
        parts = append(parts, quoteString(h))
    }
    return "[" + strings.Join(parts, ", ") + "]"
}

// mapPart maps cPanel "part" to Sieve field info.
// Handles things like "$header_subject:", "$header_from:", "$message_body".
func mapPart(part string) fieldInfo {
    p := strings.ToLower(strings.TrimSpace(part))

    // Strip Exim/cPanel-style prefixes/suffixes
    if strings.HasPrefix(p, "$header_") {
        p = strings.TrimPrefix(p, "$header_")
    }
    p = strings.TrimPrefix(p, "$")
    p = strings.TrimSuffix(p, ":")
    p = strings.TrimSpace(p)

    switch p {
    case "from", "h_from":
        return fieldInfo{kind: fieldAddress, headers: []string{"From"}}
    case "to", "h_to":
        return fieldInfo{kind: fieldAddress, headers: []string{"To"}}
    case "subject", "h_subject":
        return fieldInfo{kind: fieldHeader, headers: []string{"Subject"}}
    case "any recipient", "any_recipient", "anyrecipient":
        return fieldInfo{kind: fieldAddress, headers: []string{"To", "Cc", "Bcc"}}
    case "reply", "reply-to", "reply_to":
        return fieldInfo{kind: fieldHeader, headers: []string{"Reply-To"}}
    case "body", "message_body":
        return fieldInfo{kind: fieldBody}
    case "any header", "any_header", "anyheader":
        return fieldInfo{
            kind:    fieldHeader,
            headers: []string{"From", "To", "Cc", "Bcc", "Subject", "Reply-To"},
        }
    default:
        // Unknown part – treat as generic header name
        // Try to capitalize standard ones a bit
        if p == "" {
            p = "Subject"
        }
        return fieldInfo{kind: fieldHeader, headers: []string{p}}
    }
}

// mapMatch maps cPanel match -> (sieveOp, negative, pattern).
func mapMatch(match, val string) (sieveOp string, negative bool, bodyPattern string) {
    if val == "" {
        return "", false, ""
    }

    bodyPattern = val

    switch match {
    case "contains":
        return ":contains", false, val
    case "does not contain", "does not contains":
        return ":contains", true, val

    case "equals", "is":
        return ":is", false, val
    case "does not equal", "is not":
        return ":is", true, val

    case "begins", "begins with":
        return ":matches", false, val + "*"
    case "does not begin", "does not begin with":
        return ":matches", true, val + "*"

    case "ends", "ends with":
        return ":matches", false, "*" + val
    case "does not end", "does not end with":
        return ":matches", true, "*" + val

    default:
        return "", false, ""
    }
}

// mailboxFromDest extracts a mailbox name from a cPanel save path.
// e.g. "$home/mail/myip.gr/chris/.Nixpal" -> "Nixpal".
func mailboxFromDest(path string) string {
    if path == "" {
        return "INBOX"
    }

    base := path
    if idx := strings.LastIndex(base, "/"); idx != -1 {
        base = base[idx+1:]
    }
    base = strings.TrimSpace(base)
    base = strings.TrimPrefix(base, ".")

    if base == "" {
        return path
    }
    return base
}

// quoteString escapes a Go string into a Sieve double-quoted string
func quoteString(s string) string {
    s = strings.ReplaceAll(s, `\`, `\\`)
    s = strings.ReplaceAll(s, `"`, `\"`)
    return `"` + s + `"`
}
