package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/errorlog"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestQueryShapeDoesNotContainValues(t *testing.T) {
	for _, sql := range []string{
		`SELECT * FROM listings WHERE id='secret-value' AND price=12345 -- secret-comment`,
		`SELECT E'secret\\value', $$secret-value$$, $tag$secret-value$tag$, "secret-identifier", 12345 /* secret-comment /* nested */ */`,
		`SELECT 'secret-unclosed`,
		`SELECT $tag$secret-unclosed`,
	} {
		shape := queryShape(sql)
		if strings.Contains(shape, "secret") || strings.Contains(shape, "12345") {
			t.Fatalf("leaked: %s", shape)
		}
	}
	a := queryShape(`SELECT * FROM listings WHERE city=$1 AND price >= $2`)
	b := queryShape(`SELECT * FROM listings WHERE city=$3 AND price >= $4`)
	if a != b {
		t.Fatalf("placeholder indexes split shape: %q != %q", a, b)
	}
	if a == queryShape(`SELECT * FROM crawl_jobs WHERE city=$1`) {
		t.Fatal("different queries merged")
	}
}

// TestQueryShapeBackslashAndEscapes covers the standard_conforming_strings=on
// case where a backslash inside a plain string is an ordinary character. The
// first literal's closing quote must not be swallowed, which would expose the
// contents of the adjacent literal in the shape.
func TestQueryShapeBackslashAndEscapes(t *testing.T) {
	// In these raw literals a single backslash is an ordinary character (the
	// standard_conforming_strings=on case); the marker must never reach the shape.
	cases := []struct {
		sql    string
		marker string // must never appear in the shape
	}{
		{`SELECT 'path\' || 'SYNTHETIC_A'`, "synthetic_a"},
		{`SELECT 'a\' , 'SYNTHETIC_B'`, "synthetic_b"},
		{`SELECT E'it\'s;SYNTHETIC_C'`, "synthetic_c"},
		{`SELECT E'SYNTHETIC_D'`, "synthetic_d"},
		{`SELECT 'it''s' , 'SYNTHETIC_E'`, "synthetic_e"},
		{`SELECT "weird""col" FROM t;`, "weird"},
		{`SELECT $d$SYNTHETIC_F$d$`, "synthetic_f"},
		{`SELECT 1 -- SYNTHETIC_G`, "synthetic_g"},
		{`SELECT /* SYNTHETIC_H */ 1`, "synthetic_h"},
		{`SELECT 'SYNTHETIC_I-unclosed`, "synthetic_i"},
	}
	for _, c := range cases {
		shape := queryShape(c.sql)
		if strings.Contains(strings.ToLower(shape), c.marker) {
			t.Fatalf("leaked %q in %q for input %q", c.marker, shape, c.sql)
		}
	}
}

// TestQueryShapeStableAcrossLiteralValues proves that different runtime literal
// values do not split a signature: the shape (and therefore the hash) must be
// identical regardless of the literal text.
func TestQueryShapeStableAcrossLiteralValues(t *testing.T) {
	for _, pair := range [][2]string{
		{`SELECT * FROM listings WHERE city='Tehran' AND price > 1`, `SELECT * FROM listings WHERE city='London' AND price > 99999`},
		{`SELECT 'path\' || 'A'`, `SELECT 'path\' || 'Z'`},
		{`SELECT E'x' || 'A'`, `SELECT E'x' || 'Z'`},
		{`SELECT "col A" FROM t`, `SELECT "col B" FROM t`},
		{`SELECT $t$A$t$ FROM t`, `SELECT $t$Z$t$ FROM t`},
	} {
		if got, want := queryShape(pair[0]), queryShape(pair[1]); got != want {
			t.Fatalf("literal values split shape: %q != %q", got, want)
		}
	}
}

// TestQueryShapeSerialisedEventOmitsLiteralValues drives the real tracer and
// asserts that neither synthetic literal value appears in the emitted JSON
// event, and that the shape is stable across values.
func TestQueryShapeSerialisedEventOmitsLiteralValues(t *testing.T) {
	for _, value := range []string{"ALPHA_MARKER", "BETA_MARKER"} {
		var buf bytes.Buffer
		ctx := errorlog.New(&buf).Context(context.Background())
		sql := `SELECT 'path\' || '` + value + `';`
		ctx = (queryTracer{}).TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: sql})
		(queryTracer{}).TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: &pgconn.PgError{Code: "22012"}})
		if strings.Contains(buf.String(), value) {
			t.Fatalf("serialised event leaked literal value %q: %s", value, buf.String())
		}
	}
	// Both values must produce the same query_shape.
	shapes := map[string]bool{}
	for _, value := range []string{"ALPHA_MARKER", "BETA_MARKER"} {
		var buf bytes.Buffer
		ctx := errorlog.New(&buf).Context(context.Background())
		sql := `SELECT 'path\' || '` + value + `';`
		ctx = (queryTracer{}).TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: sql})
		(queryTracer{}).TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: &pgconn.PgError{Code: "22012"}})
		var event map[string]any
		if err := json.Unmarshal(buf.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		shapes[event["query_shape"].(string)] = true
	}
	if len(shapes) != 1 {
		t.Fatalf("expected one stable shape, got %d distinct: %v", len(shapes), shapes)
	}
}

func TestQueryTracerRedactionAndPropagation(t *testing.T) {
	var buf bytes.Buffer
	ctx := errorlog.New(&buf).Context(context.Background())
	ctx = (queryTracer{}).TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: `SELECT * FROM listings WHERE id=$1`, Args: []any{"secret-argument"}})
	(queryTracer{}).TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: &pgconn.PgError{Code: "40P01", Detail: "secret-detail"}})
	var event map[string]any
	if err := json.Unmarshal(buf.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "secret") {
		t.Fatal(buf.String())
	}
	if event["sqlstate"] != "40P01" || event["query_shape"] != "select * from listings where id= ?" {
		t.Fatal(event)
	}
}
