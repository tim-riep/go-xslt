package engine

import (
	"io"
	"strings"
	"time"

	"github.com/tim-riep/go-xslt/internal/xslt"
)

// TransformTo runs req and writes the principal result to w instead of
// returning it, so a large result never has to exist as one string. The
// returned Result carries everything else a Transform result does —
// Diagnostics, Messages, SecondaryOutputs, Method, DurationMs — with Output
// left empty, because the output is w.
//
// When the stylesheet and its xsl:output definition allow it, the result tree
// is also serialized and released AS IT IS BUILT, so that a transformation
// reading a streamed xsl:source-document and writing most of it back out runs
// in memory proportional to one record rather than to the document. Whenever
// that is not provably safe the result is built in full and then written —
// identical bytes either way, and identical to what Transform returns.
//
// Like Transform it never panics: an internal panic becomes an error
// diagnostic. Bytes already written to w before a mid-run failure are the
// caller's to discard.
func TransformTo(w io.Writer, req Request) (res Result) {
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

	return runTo(w, req)
}

func runTo(w io.Writer, req Request) Result {
	if req.Stylesheet == "" {
		return Result{Diagnostics: []Diagnostic{{
			Severity: SeverityError,
			Code:     "XTSE0010",
			Phase:    "parse",
			Message:  "empty stylesheet",
		}}}
	}

	ss, err := xslt.CompileFrom(req.Stylesheet, req.BaseDir)
	if err != nil {
		return Result{Diagnostics: []Diagnostic{diagFromErr(err, "compile")}}
	}

	source := req.Source
	if source == "" {
		source = "<_/>"
	}

	rr, terr := ss.TransformFullTo(w, source, req.Params, req.BaseDir, chunkableStylesheet(req.Stylesheet))
	if terr != nil {
		return Result{Diagnostics: []Diagnostic{diagFromErr(terr, "run")}}
	}
	res := Result{Method: rr.Method, Messages: rr.Messages}
	for _, s := range rr.Secondary {
		res.SecondaryOutputs = append(res.SecondaryOutputs, SecondaryOutput{Href: s.Href, Content: s.Content, Method: s.Method})
	}
	return res
}

// chunkableNever lists the stylesheet constructs that rule out draining the
// result tree while it is built:
//
//   - xsl:result-document, whose empty-href form redirects the PRINCIPAL output
//     and rewrites its serialization parameters in the middle of the run — far
//     too late when the declaration has already gone out;
//   - xsl:import / xsl:include / xsl:use-package, which bring in modules whose
//     text this probe never sees and which could contain one.
//
// The names are matched WITHOUT the xsl: prefix, since a stylesheet may bind
// the XSLT namespace to any prefix (or to the default namespace), and the probe
// is lexical, so a mention in a comment or inside an unrelated word is enough to
// opt out. Both kinds of over-eagerness are deliberate: being wrong in that
// direction costs a little memory, being wrong in the other costs correctness.
var chunkableNever = []string{"result-document", "import", "include", "use-package"}

func chunkableStylesheet(src string) bool {
	for _, m := range chunkableNever {
		if strings.Contains(src, m) {
			return false
		}
	}
	return true
}
