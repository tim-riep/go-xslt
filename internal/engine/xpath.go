package engine

import (
	"strings"
	"time"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// XPathRequest evaluates a standalone XPath 3.1 expression, optionally against
// an XML context document.
type XPathRequest struct {
	// Expression is the XPath 3.1 expression to evaluate.
	Expression string `json:"expression"`
	// Source is an optional XML document. When non-empty its document node is
	// the context item, so path expressions like /root/child work; when empty
	// the expression is evaluated with no context item (e.g. 1 + 2,
	// current-dateTime(), string-length('abc')).
	Source string `json:"source"`
	// BaseDir resolves relative URIs in fn:doc/fn:unparsed-text/fn:collection.
	// Empty disables disk resolution (those functions report the resource as
	// unavailable rather than erroring).
	BaseDir string `json:"baseDir"`
}

// XPathItem is one item of the result sequence.
type XPathItem struct {
	// Type is a short label: a node kind ("element", "attribute", …), an atomic
	// type QName ("xs:integer", …), or "map"/"array"/"function".
	Type string `json:"type"`
	// Value is the item's string value (element/attribute string value; atomic
	// lexical form).
	Value string `json:"value"`
}

// XPathResult is the outcome of an XPath evaluation.
type XPathResult struct {
	// Value is the string value of the whole result sequence.
	Value string `json:"value"`
	// Items is one entry per item in the result sequence.
	Items []XPathItem `json:"items"`
	// Count is the number of items in the result sequence.
	Count int `json:"count"`
	// Diagnostics carries parse/eval errors (non-empty on failure).
	Diagnostics []Diagnostic `json:"diagnostics"`
	// DurationMs is wall-clock evaluation time in milliseconds.
	DurationMs int64 `json:"durationMs"`
}

// HasErrors reports whether the result contains any error-severity diagnostic.
func (r XPathResult) HasErrors() bool {
	for _, d := range r.Diagnostics {
		if d.Severity == SeverityError {
			return true
		}
	}
	return false
}

// EvalXPath parses and evaluates req.Expression. It never panics: any internal
// panic is recovered and surfaced as an error diagnostic.
func EvalXPath(req XPathRequest) (res XPathResult) {
	start := time.Now()
	defer func() {
		if r := recover(); r != nil {
			res.Diagnostics = append(res.Diagnostics, Diagnostic{
				Severity: SeverityError,
				Code:     "INTERNAL",
				Phase:    "run",
				Message:  "internal engine error: " + toString(r),
			})
		}
		res.DurationMs = time.Since(start).Milliseconds()
	}()
	return runXPath(req)
}

func runXPath(req XPathRequest) XPathResult {
	parsed, err := xpath.Parse(req.Expression)
	if err != nil {
		return XPathResult{Diagnostics: []Diagnostic{xpathDiag("parse", err)}}
	}
	ctx := &xpath.Context{Pos: 1, Size: 1}
	if strings.TrimSpace(req.Source) != "" {
		// ParseLenient11WithBase (not the plain Parse EvalXPath used before)
		// so a DOCTYPE's external SYSTEM subset resolves against BaseDir too,
		// consistent with how the XSLT engine parses its own source document.
		doc, perr := xmltree.ParseLenient11WithBase(req.Source, req.BaseDir)
		if perr != nil {
			return XPathResult{Diagnostics: []Diagnostic{xpathDiag("parse", perr)}}
		}
		ctx.Node = doc
	}
	if strings.TrimSpace(req.BaseDir) != "" {
		ctx.Resolver = newStandaloneResolver(req.BaseDir)
		ctx.BaseURI = baseDirURI(req.BaseDir)
	}
	v, eerr := parsed.Eval(ctx)
	if eerr != nil {
		return XPathResult{Diagnostics: []Diagnostic{xpathDiag("run", eerr)}}
	}
	items := xpath.Items(v)
	res := XPathResult{Count: len(items)}
	parts := make([]string, len(items))
	for i, it := range items {
		s := xpath.ItemString(it)
		parts[i] = s
		res.Items = append(res.Items, XPathItem{Type: xpath.ItemKind(it), Value: s})
	}
	// Build the whole-sequence preview from the per-item host strings rather
	// than xpath.ToString, which is empty for maps/arrays (they have no string
	// value) and returns only the first item for multi-item sequences.
	res.Value = strings.Join(parts, "\n")
	return res
}

// xpathDiag turns an XPath parse/eval error into a Diagnostic, extracting a
// leading "err:CODE:" prefix (or a bare "XPST0003"-shaped token) into Code.
func xpathDiag(phase string, err error) Diagnostic {
	msg := err.Error()
	code := ""
	if rest, ok := strings.CutPrefix(msg, "err:"); ok {
		if i := strings.IndexByte(rest, ':'); i > 0 {
			code = rest[:i]
			msg = strings.TrimSpace(rest[i+1:])
		}
	}
	return Diagnostic{Severity: SeverityError, Phase: phase, Code: code, Message: msg}
}
