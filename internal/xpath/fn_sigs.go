package xpath

import (
	"strconv"
	"strings"
	"sync"
)

// Declared signatures of common built-in functions, so that a named function
// reference such as name#1 carries its parameter and return types and typed
// function tests can judge it precisely (instanceof128..130: name#1 is NOT a
// function(item()) as xs:string; ArrayTest-064/084: [floor#1, …, name#1] is
// not an array(function(xs:numeric?) as xs:numeric?)). Function COERCION of
// arguments stays arity-based (convertForParam), so a typed built-in passed
// where a different function type is expected is still accepted and checked
// when called. Keyed by "local/arity"; the value is "p1, p2 -> ret".
var sigStdSrc = map[string]string{
	"name/1":            "node()? -> xs:string",
	"local-name/1":      "node()? -> xs:string",
	"namespace-uri/1":   "node()? -> xs:anyURI",
	"string/1":          "item()? -> xs:string",
	"string-length/1":   "xs:string? -> xs:integer",
	"normalize-space/1": "xs:string? -> xs:string",
	"upper-case/1":      "xs:string? -> xs:string",
	"lower-case/1":      "xs:string? -> xs:string",
	"abs/1":             "xs:numeric? -> xs:numeric?",
	"floor/1":           "xs:numeric? -> xs:numeric?",
	"ceiling/1":         "xs:numeric? -> xs:numeric?",
	"round/1":           "xs:numeric? -> xs:numeric?",
	"number/1":          "xs:anyAtomicType? -> xs:double",
	"count/1":           "item()* -> xs:integer",
	"exists/1":          "item()* -> xs:boolean",
	"empty/1":           "item()* -> xs:boolean",
	"not/1":             "item()* -> xs:boolean",
	"boolean/1":         "item()* -> xs:boolean",
	"true/0":            "-> xs:boolean",
	"false/0":           "-> xs:boolean",
	"concat/2":          "xs:anyAtomicType?, xs:anyAtomicType? -> xs:string",
	"contains/2":        "xs:string?, xs:string? -> xs:boolean",
	"starts-with/2":     "xs:string?, xs:string? -> xs:boolean",
	"ends-with/2":       "xs:string?, xs:string? -> xs:boolean",
	"substring/2":       "xs:string?, xs:double -> xs:string",
	"data/1":            "item()* -> xs:anyAtomicType*",
	// fn:filter's predicate parameter is a specific function type (not just
	// "any function"), so a named reference filter#2 correctly judges as NOT
	// an instance of a broader/different function(*) signature
	// (instanceof134: function(function(*), item()*) as item()* widens both
	// the predicate parameter and the input parameter's position, and
	// function-type parameter subtyping is CONTRAVARIANT — the candidate
	// type's own parameter must be a supertype of filter's real one, which
	// function(*) is not of function(item()) as xs:boolean).
	"filter/2": "item()*, function(item()) as xs:boolean -> item()*",
}

type sigStd struct {
	params []*SeqType
	ret    *SeqType
}

var (
	sigOnce  sync.Once
	sigTable map[string]sigStd
)

// sigParseSeqType parses one SequenceType from its lexical form.
func sigParseSeqType(s string) (*SeqType, bool) {
	toks, err := lex(strings.TrimSpace(s))
	if err != nil {
		return nil, false
	}
	pr := &parser{toks: toks}
	st, err := pr.parseSequenceType()
	if err != nil || pr.cur().kind != tEOF {
		return nil, false
	}
	return st, true
}

func sigBuild() {
	sigTable = map[string]sigStd{}
	for key, src := range sigStdSrc {
		ps, ret, ok := strings.Cut(src, "->")
		if !ok {
			continue
		}
		rt, ok := sigParseSeqType(ret)
		if !ok {
			continue
		}
		var params []*SeqType
		if strings.TrimSpace(ps) != "" {
			good := true
			for _, p := range strings.Split(ps, ",") {
				st, ok := sigParseSeqType(p)
				if !ok {
					good = false
					break
				}
				params = append(params, st)
			}
			if !good {
				continue
			}
		}
		sigTable[key] = sigStd{params: params, ret: rt}
	}
}

// sigApply attaches the declared signature of an fn-namespace function to a
// named function reference, when one is catalogued.
func sigApply(f *Function) *Function {
	if f == nil || f.NS != nsFn {
		return f
	}
	sigOnce.Do(sigBuild)
	sg, ok := sigTable[f.Name+"/"+strconv.Itoa(f.Arity)]
	if !ok || len(sg.params) != f.Arity {
		return f
	}
	f.Typed, f.Params, f.Ret = true, sg.params, sg.ret
	return f
}
