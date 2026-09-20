package xpath

import "github.com/tim-riep/go-xslt/internal/xmltree"

// Expr is a parsed XPath expression node.
type Expr interface{ expr() }

// LiteralExpr is a string or number literal (Val is string or float64).
type LiteralExpr struct{ Val Object }

// VarRef is a variable reference $name (Prefix optional).
type VarRef struct {
	Prefix string
	Local  string
}

// FuncCall is a function invocation. Name keeps the (possibly prefixed) QName.
type FuncCall struct {
	Prefix string
	Local  string
	Args   []Expr
}

// BinaryExpr is an infix operation: and, or, =, !=, <, <=, >, >=, +, -, *, div, mod.
type BinaryExpr struct {
	Op   string
	L, R Expr
}

// UnaryExpr is arithmetic negation, or (Plus) a unary plus — which does not
// change the value but still requires a numeric operand.
type UnaryExpr struct {
	X    Expr
	Plus bool
}

// UnionExpr is a node-set union (|).
type UnionExpr struct{ L, R Expr }

// PathExpr is a location path, optionally rooted at a primary filter expression.
type PathExpr struct {
	// Start is nil for a pure location path. When set, the path is a filter
	// expression (primary + predicates) followed by relative steps.
	Start    Expr
	Absolute bool // leading '/'
	Steps    []*Step
	// predPattern marks the synthetic self::node() path ParsePattern builds
	// for an XSLT PredicatePattern ("." followed by one or more predicates).
	// It exists only so the XSLT default-priority table can give that pattern
	// its own value; it is never set by ordinary expression parsing.
	predPattern bool
}

// FilterExpr is a primary expression with trailing predicates.
type FilterExpr struct {
	Primary Expr
	Preds   []Expr
}

// testKind classifies a node test.
type testKind int

const (
	testName          testKind = iota // element/attribute name test (possibly wildcard)
	testNode                          // node()
	testText                          // text()
	testComment                       // comment()
	testPI                            // processing-instruction('target'?)
	testElement                       // element() / element(name) / element(name,type)
	testAttribute                     // attribute() / attribute(name) / attribute(name,type)
	testDocument                      // document-node() / document-node(element(...))
	testNamespace                     // namespace-node()
	testSchemaElement                 // schema-element(name)  (never matches: no schema)
	testSchemaAttr                    // schema-attribute(name) (never matches: no schema)
)

// NodeTest is the node test of a step (and the node ItemType of a SequenceType).
type NodeTest struct {
	Kind     testKind
	AnyName  bool      // '*' (wildcard, incl. prefix:* / *:local when combined with Prefix/Local)
	WildNS   bool      // '*:local' (any namespace, specific local)
	Prefix   string    // namespace prefix (unresolved); "" if none
	URI      string    // explicit namespace URI (from Q{uri}local); "" if none
	Braced   bool      // the name was written as Q{uri}local: URI is authoritative even when ""
	Local    string    // local name for a name test
	PITarget string    // optional literal target for processing-instruction()
	Inner    *NodeTest // nested element test for document-node(element(...))
	TypeName string    // optional element/attribute type name
	// SchemaType is TypeName RESOLVED against an imported schema — the
	// declared type of a schema-element()/schema-attribute() test, or the
	// named type of element(N,T)/attribute(N,T) when T is a user-defined
	// component rather than a built-in. It is filled in at PARSE time by the
	// host-supplied SchemaNameLookup (see schema_names.go) and is nil for
	// every ordinary, schema-unaware parse, so matching falls back to the
	// built-in-only reading exactly as before.
	SchemaType *xmltree.SchemaTypeName
	// TypedElem marks a plain element NAME test that xsl:mode/@typed=
	// "strict"/"lax" has additionally bound to a global element declaration
	// (see Pattern.TypedModeRewrite): the test then matches like
	// schema-element(N) — substitution group included, SchemaType derivation
	// required — while keeping the default priority and streamability
	// classification of the name test it was written as.
	TypedElem bool
	// Nillable records the optional "?" of element(N, T?): the test then also
	// matches a NILLED element, which element(N, T) does not (XPath 3.1
	// §2.5.5.3 clause 3). It is meaningless without a TypeName, exactly as the
	// grammar has it.
	Nillable bool
}

// Step is one location step: axis + node test + predicates.
type Step struct {
	Axis string // child, descendant, parent, self, attribute, ...
	// AxisExplicit records that the axis was WRITTEN ("child::", "@",
	// "attribute::", ...) rather than defaulted. XSLT pattern semantics
	// (§5.5.3) distinguish the two: a document-node() test with no explicit
	// axis is evaluated on the self axis, while "child::document-node()"
	// keeps the child axis and therefore matches nothing.
	AxisExplicit bool
	Test         NodeTest
	Preds        []Expr
	// Postfix is a non-axis step: a PostfixExpr (e.g. a function call like
	// xs:decimal(.) or $f(.)) used as a relative-path step. When set, Axis/Test
	// are unused and the expression is evaluated with each context node as focus.
	Postfix Expr
}

// SequenceExpr is a comma-separated sequence constructor: (a, b, c).
type SequenceExpr struct{ Items []Expr }

// RangeExpr is the integer range operator: from to to.
type RangeExpr struct{ From, To Expr }

// VarBind binds a variable to a sequence (for/let/quantified clauses).
type VarBind struct {
	Prefix string
	Local  string
	Seq    Expr
}

// ForExpr is "for $v in seq[, ...] return body".
type ForExpr struct {
	Binds []VarBind
	Body  Expr
}

// LetExpr is "let $v := seq[, ...] return body" (XPath 3.0).
type LetExpr struct {
	Binds []VarBind
	Body  Expr
}

// QuantExpr is "some|every $v in seq[, ...] satisfies test".
type QuantExpr struct {
	Every     bool
	Binds     []VarBind
	Satisfies Expr
}

// IfExpr is "if (cond) then a else b".
type IfExpr struct{ Cond, Then, Else Expr }

// MapExpr is a map constructor: map { k : v, ... }.
type MapExpr struct {
	Keys []Expr
	Vals []Expr
}

// ArrayExpr is a square ([a,b]) or curly (array{seq}) array constructor.
type ArrayExpr struct {
	Items []Expr
	Curly bool // array { expr } flattens the sequence into members
}

// InlineFunc is an inline function expression: function($x) { body }.
type InlineFunc struct {
	Params     []VarBind  // only Prefix/Local used
	ParamTypes []*SeqType // declared param types (nil element = untyped)
	RetType    *SeqType   // declared "as" return type (nil = none)
	Body       Expr
}

// LookupExpr is the postfix lookup operator: base?key (maps/arrays).
type LookupExpr struct {
	Base Expr
	Key  Expr // nil when Wildcard
	Wild bool // base?*
}

// DynCall is a dynamic function call: base(args) where base is a function item.
type DynCall struct {
	Base Expr
	Args []Expr
}

// ContextItemExpr is the context-item expression ".".
type ContextItemExpr struct{}

// compKind classifies a comparison.
type compKind int

const (
	compGeneral compKind = iota // = != < <= > >=
	compValue                   // eq ne lt le gt ge
	compNode                    // is << >>
)

// CompareExpr is a (non-chaining) comparison.
type CompareExpr struct {
	Kind compKind
	Op   string
	L, R Expr
}

// StringConcatExpr is the || operator over two-or-more operands.
type StringConcatExpr struct{ Parts []Expr }

// IntersectExceptExpr is the intersect/except node-set operator.
type IntersectExceptExpr struct {
	Op   string // "intersect" | "except"
	L, R Expr
}

// InstanceOfExpr is "x instance of SeqType".
type InstanceOfExpr struct {
	X    Expr
	Type *SeqType
}

// TreatExpr is "x treat as SeqType".
type TreatExpr struct {
	X    Expr
	Type *SeqType
}

// CastExpr is "x cast as T" or "x castable as T" (Castable distinguishes).
type CastExpr struct {
	X        Expr
	Type     *SeqType // a SingleType (atomic + optional '?')
	Castable bool
}

// ArrowStep is one "=> spec(args)" in an arrow expression.
type ArrowStep struct {
	Spec Expr   // function specifier: EQName(FuncCall w/ no args carrier)|VarRef|Parenthesized
	Name string // set when Spec is an EQName function name
	Pre  string // prefix for the EQName
	Args []Expr
}

// ArrowExpr is "base => f(args) => g(args) ...".
type ArrowExpr struct {
	Base  Expr
	Steps []ArrowStep
}

// SimpleMapExpr is "e1 ! e2 ! ...".
type SimpleMapExpr struct{ Steps []Expr }

// NamedFuncRef is "EQName#arity".
type NamedFuncRef struct {
	Prefix string
	Local  string
	URI    string // set when written as the EQName braced form Q{uri}local
	Arity  int
}

// Placeholder is an argument placeholder "?" (partial application).
type Placeholder struct{}

func (*ContextItemExpr) expr()     {}
func (*CompareExpr) expr()         {}
func (*StringConcatExpr) expr()    {}
func (*IntersectExceptExpr) expr() {}
func (*InstanceOfExpr) expr()      {}
func (*TreatExpr) expr()           {}
func (*CastExpr) expr()            {}
func (*ArrowExpr) expr()           {}
func (*SimpleMapExpr) expr()       {}
func (*NamedFuncRef) expr()        {}
func (*Placeholder) expr()         {}

func (*SequenceExpr) expr() {}
func (*RangeExpr) expr()    {}
func (*ForExpr) expr()      {}
func (*LetExpr) expr()      {}
func (*QuantExpr) expr()    {}
func (*IfExpr) expr()       {}
func (*MapExpr) expr()      {}
func (*ArrayExpr) expr()    {}
func (*InlineFunc) expr()   {}
func (*LookupExpr) expr()   {}
func (*DynCall) expr()      {}

func (*LiteralExpr) expr() {}
func (*VarRef) expr()      {}
func (*FuncCall) expr()    {}
func (*BinaryExpr) expr()  {}
func (*UnaryExpr) expr()   {}
func (*UnionExpr) expr()   {}
func (*PathExpr) expr()    {}
func (*FilterExpr) expr()  {}
