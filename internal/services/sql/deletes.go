package sql

import (
	"encoding/json"
	"strings"
	"unicode"
)

// deletesOnly reports whether every statement only deletes (ADR-0292 Decision 4), which a database at its quota still
// runs: each statement's text holds one statement, nothing but comments and whitespace after its first ";", whose
// first keyword is DELETE or DROP. Anything else, a WITH … DELETE included, is a write.
func deletesOnly(stmts []json.RawMessage) bool {
	for _, raw := range stmts {
		var parts []json.RawMessage
		var text string
		if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 || json.Unmarshal(parts[0], &text) != nil {
			return false
		}
		if !deletes(text) {
			return false
		}
	}
	return len(stmts) > 0
}

func deletes(text string) bool {
	rest := skipBlank(text)
	end := strings.IndexFunc(rest, func(r rune) bool { return !unicode.IsLetter(r) })
	if end < 0 {
		end = len(rest)
	}
	if kw := rest[:end]; !strings.EqualFold(kw, "DELETE") && !strings.EqualFold(kw, "DROP") {
		return false
	}
	i := statementEnd(rest)
	return i < 0 || skipBlank(rest[i+1:]) == ""
}

// skipBlank drops leading whitespace, -- comments and /* */ comments.
func skipBlank(s string) string {
	for {
		s = strings.TrimLeft(s, " \t\r\n\f\v")
		switch {
		case strings.HasPrefix(s, "--"):
			i := strings.IndexByte(s, '\n')
			if i < 0 {
				return ""
			}
			s = s[i+1:]
		case strings.HasPrefix(s, "/*"):
			i := strings.Index(s[2:], "*/")
			if i < 0 {
				return ""
			}
			s = s[i+4:]
		default:
			return s
		}
	}
}

// statementEnd is the index of the first ";" outside a literal, a quoted identifier and a comment, or -1.
func statementEnd(s string) int {
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == ';':
			return i
		case c == '\'' || c == '"' || c == '`':
			j := closeQuote(s, i+1, c)
			if j < 0 {
				return -1
			}
			i = j
		case c == '[':
			j := strings.IndexByte(s[i+1:], ']')
			if j < 0 {
				return -1
			}
			i += j + 1
		case strings.HasPrefix(s[i:], "--"):
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				return -1
			}
			i += j
		case strings.HasPrefix(s[i:], "/*"):
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				return -1
			}
			i += j + 3
		}
	}
	return -1
}

// closeQuote is the index of the quote q closing the one before from, a doubled quote escaping it, or -1.
func closeQuote(s string, from int, q byte) int {
	for i := from; i < len(s); i++ {
		if s[i] != q {
			continue
		}
		if i+1 < len(s) && s[i+1] == q {
			i++
			continue
		}
		return i
	}
	return -1
}
