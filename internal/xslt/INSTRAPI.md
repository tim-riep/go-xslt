# xslt package — instruction-implementation API contract

Read fully before writing code. All code goes in package `xslt` under
`internal/xslt/`. Implement one XSLT 3.0 instruction (or small family) in a new
file `instr_<name>.go` with tests in `instr_<name>_test.go`.

## Registering

- **Instruction** (an `xsl:` element used in a sequence constructor): register a
  compiler in `init()`, and make the compiled struct implement `instr()` +
  `exec`:
  ```go
  func init() {
      instrRegistry["for-each-group"] = func(c *compiler, el *xmltree.Node) (instruction, error) { ... }
  }
  type forEachGroup struct{ /* compiled fields */ }
  func (*forEachGroup) instr() {}
  func (n *forEachGroup) exec(eng *engine, r rt, out *xmltree.Node) error { ... }
  ```
- **Top-level declaration** (child of xsl:stylesheet): register in `declRegistry`:
  ```go
  declRegistry["accumulator"] = func(c *compiler, ss *Stylesheet, el *xmltree.Node) error { ... }
  ```
- **Context function** (e.g. current-group()): register in `xsltFuncs`:
  ```go
  xsltFuncs["current-group"] = func(eng *engine, args []xpath.Object, env *evalEnv) (xpath.Object, bool, error) {
      return eng.curGroup, true, nil
  }
  ```

## Compile-time helpers (methods on *compiler)

- `c.compileSequence(nodes []*xmltree.Node) ([]instruction, error)` — compile a
  body (text+elements). Use for child content.
- `c.compileSortsAndParams(el) ([]sortKey, []*VarDef, error)` — collect child
  `xsl:sort` + `xsl:with-param`.
- `c.compileVarDef(el *xmltree.Node, isParam bool) (*VarDef, error)` — compile an
  `xsl:param`/`xsl:variable`/`xsl:with-param`.
- `requireExpr(el, attr) (*xpath.Parsed, error)`, `requireAVT(el, attr) (*avt, error)`,
  `parseAVT(s) (*avt, error)`.
- `elementChildren(n)`, `childNodesForBody(el)` (text+elements minus param/sort/
  with-param), `nonSortChildren(el)`.
- `resolveQName(el, q) xmltree.Name`, `errAt(el, format, args...) error`,
  `el.AttrLocal("name") (string,bool)`.
- `parseXPathFor(el, src) (*xpath.Parsed, error)` / `parsePatternFor(el, src)
  (*xpath.Pattern, error)` — use these, NOT `xpath.Parse`/`xpath.ParsePattern`,
  for an expression or pattern written on a stylesheet element. They resolve
  any schema component name in it (`element(N,T)`, `schema-element(N)`)
  against the components `xsl:import-schema` brought in and el's own namespace
  bindings; with no schema imported they are exactly `xpath.Parse`/
  `xpath.ParsePattern` (decl_import_schema.go).

## Run-time helpers (methods on *engine)

- `eng.eval(p *xpath.Parsed, el *xmltree.Node, r rt) (xpath.Object, error)` and
  `eng.evalString(p, el, r) (string, error)` — evaluate XPath in element el's
  namespace context. Variable refs resolve against the engine's scopes, so to
  evaluate with extra variables: `eng.pushScope(); eng.bindVar(name, val); … eng.eval(...); eng.popScope()`.
- `eng.execSequence(instrs []instruction, r rt, out *xmltree.Node) error`.
- `eng.pushScope()`, `eng.popScope()`, `eng.bindVar(name xmltree.Name, v xpath.Object)`,
  `eng.evalVarDef(vd *VarDef, r rt) (xpath.Object, error)`.
- `eng.evalAVT(a *avt, el, r) (string, error)`, `eng.stringFromBody(body []instruction, r rt) (string, error)`.
- `eng.applyToNodes(nodes xpath.NodeSet, mode string, out *xmltree.Node, params []*VarDef) error`.
- `eng.sortNodes(nodes []*xmltree.Node, sorts []sortKey, el *xmltree.Node) ([]*xmltree.Node, error)`.
- `rt` is `struct{ node *xmltree.Node; pos, size int }`. Make a child position:
  `rt{node: n, pos: i + 1, size: total}`.

## Engine state you may use

`eng.doc` (source document), `eng.baseDir` (workspace dir; "" if none),
`eng.messages []string` (append xsl:message text), `eng.secondary []SecondaryDoc`
(append `SecondaryDoc{Href, Content, Method}` for xsl:result-document),
`eng.curGroup xpath.NodeSet`/`eng.curGroupOK bool`/`eng.curKey xpath.Object`
(for-each-group), `eng.curMergeGroup`/`eng.curMergeKey` (merge),
`eng.accCache map[string]any` and `eng.scratch map[string]any` (your own state;
initialise lazily: `if eng.scratch == nil { eng.scratch = map[string]any{} }`).

## Building result nodes

`out.Append(child *xmltree.Node)`; `xmltree.NewElement(xmltree.Name{Local,Space,Prefix})`;
`xmltree.NewText(s)`; `deepCopyInto(node, out)` (deep copy a node into out);
`&xmltree.Node{Kind: xmltree.KindComment, Value: s}`. To run a body into a
temporary tree (e.g. xsl:try, xsl:variable RTF): `frag := &xmltree.Node{Kind: xmltree.KindDocument}; eng.execSequence(body, r, frag)` then append `frag.Children`.

## XPath values (package xpath)

`xpath.Object` (=any), `xpath.NodeSet` (=[]*xmltree.Node), `xpath.Sequence`,
`xpath.Items(o) []Item` / `xpath.FromItems([]Item) Object`,
`xpath.ToString/ToNumber/ToBool/ToNodeSet`, `xpath.NewInteger(int64)`,
`xpath.NewString`, `xpath.NewMap()` + `(*Map).Put(key Item, val Object)`,
`xpath.NewArray([]Object)`, `xpath.Atomize(o) ([]Item, error)`.

## Flow control (xsl:iterate / break / next-iteration)

Define sentinel error types in YOUR file and catch them in the iterate loop:
```go
type sqBreak struct{ val xpath.Object }
func (sqBreak) Error() string { return "xsl:break" }
```
xsl:next-iteration/xsl:break exec returns the sentinel; the iterate loop checks
`errors.As`. This avoids shared engine fields.

## Tests

Use the existing helper `transform(t, sheet, src string, params map[string]string) string`
(defined in transform_test.go) — it compiles + transforms and returns the
serialized output. Stylesheet header:
```
<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="text"/>  (or xml)
  ...
</xsl:stylesheet>
```
Drive instructions from a `match="/r"` (or `match="/"` + select="…") template.

## Rules

1. ONE `instr_<name>.go` + ONE `instr_<name>_test.go`. Do not edit other files.
2. Prefix all private helpers/types with your family initials (e.g. `feg`, `itr`,
   `mrg`, `num`, `tc`, `ev`, `msg`, `rd`, `acc`, `mp`, `ps`).
3. Do NOT redefine existing helpers/types (instruction, execer, rt, engine,
   compiler, evalEnv, avt, sortKey, VarDef, errAt, resolveQName, clark,
   childNodesForBody, deepCopyInto, etc.).
4. Do NOT run go build/test (siblings are written concurrently). Write correct
   code per this contract.
5. stdlib + goxslt/internal/xpath + goxslt/internal/xmltree only.
