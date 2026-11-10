package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"runtime"
	"strings"
	"unicode"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/errorlog"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type queryDiagnosticKey struct{}
type queryTracer struct{}

func (queryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	shape := queryShape(data.SQL)
	sum := sha256.Sum256([]byte(shape))
	name := "postgres.query"
	var pcs [24]uintptr
	n := runtime.Callers(2, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		if strings.Contains(f.Function, "/outbound/postgres.") && !strings.Contains(f.Function, "queryTracer") {
			name = strings.TrimPrefix(f.Function, "github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/")
			break
		}
		if !more {
			break
		}
	}
	if len(shape) > 1024 {
		shape = shape[:1024] + " …"
	}
	d := ports.Diagnostic{Component: "postgres", Operation: name, QueryName: name + ":" + hex.EncodeToString(sum[:8]), QueryShape: shape}
	return context.WithValue(errorlog.Context(ctx), queryDiagnosticKey{}, d)
}

func (queryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if data.Err == nil || errors.Is(data.Err, pgx.ErrNoRows) {
		return
	}
	var pg *pgconn.PgError
	if errors.As(data.Err, &pg) && pg.Code == "25P02" {
		return
	} // Secondary aborted-transaction noise.
	if d, ok := ctx.Value(queryDiagnosticKey{}).(ports.Diagnostic); ok {
		ports.ReportDiagnosticOccurrence(ctx, d, data.Err)
	}
}

var dollarQuote = regexp.MustCompile(`^\$(?:[A-Za-z_][A-Za-z_0-9]*)?\$`)

// isIdentChar reports whether the byte can follow an identifier start without
// terminating the identifier (a word character or '_').
func isIdentChar(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// scanESString consumes an E'...' escape string. Backslash escapes are active,
// so a backslash before the closing quote extends the string. It returns the
// index just past the terminating quote, or len(sql) if the token is unclosed.
func scanESString(sql string, i int) int {
	for i < len(sql) {
		switch sql[i] {
		case '\\':
			i += 2 // escaped character (including an escaped quote) extends the string
		case '\'':
			i++
			if i < len(sql) && sql[i] == '\'' {
				i++ // doubled quote is part of the string
				continue
			}
			return i
		default:
			i++
		}
	}
	return len(sql)
}

// scanPlainString consumes a '...' string. With the default
// standard_conforming_strings=on a backslash is an ordinary character and only
// a doubled quote extends the string; any backslash is left to terminate on the
// next plain quote. It returns the index just past the terminating quote, or
// len(sql) if the token is unclosed.
func scanPlainString(sql string, i int) int {
	for i < len(sql) {
		if sql[i] == '\'' {
			i++
			if i < len(sql) && sql[i] == '\'' {
				i++ // doubled quote is part of the string
				continue
			}
			return i
		}
		i++
	}
	return len(sql)
}

// scanQuotedIdentifier consumes a "..." identifier. Backslash escaping is not
// valid inside quoted identifiers and a doubled quote extends the identifier.
// It returns the index just past the terminating quote, or len(sql) if
// unclosed.
func scanQuotedIdentifier(sql string, i int) int {
	for i < len(sql) {
		if sql[i] == '"' {
			i++
			if i < len(sql) && sql[i] == '"' {
				i++ // doubled quote is part of the identifier
				continue
			}
			return i
		}
		i++
	}
	return len(sql)
}

// queryShape is a conservative SQL lexer: it strips comments and literal values
// using PostgreSQL token rules (E-strings, standard strings, quoted
// identifiers, dollar quotes) without ever examining query arguments. Redacted
// literals become a single placeholder so different literal values do not split
// a signature. Unclosed tokens consume the remainder so malformed SQL cannot
// leak their contents; a token that cannot be terminated fails closed to a
// single placeholder that swallows the rest of the statement.
func queryShape(sql string) string {
	var out strings.Builder
	for i := 0; i < len(sql); {
		switch {
		case strings.HasPrefix(sql[i:], "--"):
			end := strings.IndexByte(sql[i:], '\n')
			if end < 0 {
				i = len(sql)
			} else {
				i += end + 1
			}
			out.WriteByte(' ')
		case strings.HasPrefix(sql[i:], "/*"):
			depth := 1
			i += 2
			for i < len(sql) && depth > 0 {
				if strings.HasPrefix(sql[i:], "/*") {
					depth++
					i += 2
				} else if strings.HasPrefix(sql[i:], "*/") {
					depth--
					i += 2
				} else {
					i++
				}
			}
			out.WriteByte(' ')
		// E'...' escape string: an 'E' immediately before a single quote, where
		// E is not part of a longer word (so "WHERE 'x'" is unaffected).
		case sql[i] == 'E' || sql[i] == 'e':
			if i+1 < len(sql) && sql[i+1] == '\'' && (i == 0 || !isIdentChar(sql[i-1])) {
				out.WriteString(" e ? ")
				i = scanESString(sql, i+2)
			} else {
				start := i
				i++
				for i < len(sql) && (unicode.IsLetter(rune(sql[i])) || unicode.IsDigit(rune(sql[i])) || sql[i] == '_') {
					i++
				}
				out.WriteString(strings.ToLower(sql[start:i]))
			}
		case sql[i] == '\'':
			i = scanPlainString(sql, i+1)
			out.WriteString(" ? ")
		case sql[i] == '"':
			i = scanQuotedIdentifier(sql, i+1)
			out.WriteString(" ? ")
		case sql[i] == '$':
			if tag := dollarQuote.FindString(sql[i:]); tag != "" {
				i += len(tag)
				end := strings.Index(sql[i:], tag)
				if end < 0 {
					i = len(sql)
				} else {
					i += end + len(tag)
				}
				out.WriteString(" ? ")
			} else {
				i++
				for i < len(sql) && sql[i] >= '0' && sql[i] <= '9' {
					i++
				}
				out.WriteString(" ? ")
			}
		case sql[i] >= '0' && sql[i] <= '9':
			i++
			for i < len(sql) && strings.ContainsRune("0123456789.eE+-", rune(sql[i])) {
				i++
			}
			out.WriteString(" ? ")
		case unicode.IsLetter(rune(sql[i])) || sql[i] == '_':
			start := i
			i++
			for i < len(sql) && (unicode.IsLetter(rune(sql[i])) || unicode.IsDigit(rune(sql[i])) || sql[i] == '_') {
				i++
			}
			out.WriteString(strings.ToLower(sql[start:i]))
		default:
			out.WriteByte(sql[i])
			i++
		}
	}
	return strings.Join(strings.Fields(out.String()), " ")
}
