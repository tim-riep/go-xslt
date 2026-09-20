package xpath

import (
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

func init() {
	coreFuncs["error"] = fnError
	coreFuncs["trace"] = fnTrace
	mathFuncs["exp10"] = mathExp10
}

// fnError implements fn:error (arities 0-3). It always raises a dynamic error;
// the optional first argument is an xs:QName error code, the second a
// description, the third an error object (ignored here).
func fnError(_ *Context, args []Object) (Object, error) {
	qn := xmltree.Name{Space: nsErr, Local: "FOER0000", Prefix: "err"}
	code := "err:FOER0000"
	if len(args) >= 1 && !numIsEmpty(arg(args, 0)) {
		if q, ok := firstItem(arg(args, 0)).(*Atomic); ok && q.T == XSqname {
			if q.qn.Local != "" {
				qn = q.qn
				code = "err:" + q.qn.Local
			}
		}
	}
	desc := ""
	if len(args) >= 2 && !numIsEmpty(arg(args, 1)) {
		desc = ": " + itemString(firstItem(arg(args, 1)))
	}
	var value Object
	if len(args) >= 3 {
		value = arg(args, 2)
	}
	return nil, &Error{Code: qn, Desc: strings.TrimPrefix(desc, ": "), Value: value, msg: code + desc}
}

// nsErr is the namespace of the standard XPath/XQuery/XSLT error codes.
const nsErr = "http://www.w3.org/2005/xqt-errors"

// Error is a STRUCTURED dynamic error: it carries fn:error's error QName,
// description and error object alongside the flat message string every other
// error in this package uses. Its Error() text is byte-for-byte what an
// unstructured fmt.Errorf would have produced, so nothing that merely reads
// err.Error() (error-code extraction in the conformance harnesses, the XSLT
// engine's own "err:CODE:" scan) sees any difference; a host that wants the
// parts — XSLT's xsl:catch, which must bind $err:code as a real xs:QName and
// $err:value as the error object — type-asserts to *Error instead.
type Error struct {
	Code  xmltree.Name
	Desc  string
	Value Object
	msg   string
}

func (e *Error) Error() string { return e.msg }

// NewError builds a structured dynamic error with the given error QName,
// description and error object. The flat message keeps the conventional
// "err:LOCAL: description" shape every error-code scan in this project relies
// on. Hosts use it to raise an error whose parts xsl:catch can bind
// ($err:code / $err:description / $err:value) — XSLT's xsl:message
// terminate="yes" (XTMM9000) is one.
func NewError(code xmltree.Name, desc string, value Object) *Error {
	msg := "err:" + code.Local
	if desc != "" {
		msg += ": " + desc
	}
	return &Error{Code: code, Desc: desc, Value: value, msg: msg}
}

// fnTrace implements fn:trace($value, $label?). It returns its first argument
// unchanged, emitting the value (and optional label) to stderr as a side effect.
func fnTrace(_ *Context, args []Object) (Object, error) {
	v := arg(args, 0)
	label := "trace"
	if len(args) >= 2 {
		label = itemString(firstItem(arg(args, 1)))
	}
	fmt.Fprintf(os.Stderr, "[%s] %s\n", label, ToString(v))
	return v, nil
}

// mathExp10 implements math:exp10($x) = 10^x.
func mathExp10(_ *Context, args []Object) (Object, error) {
	if numIsEmpty(arg(args, 0)) {
		return Sequence{}, nil
	}
	return NewDouble(math.Pow(10, ToNumber(arg(args, 0)))), nil
}
