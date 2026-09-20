package xpath

import "fmt"

// Arity catalog for fn:function-lookup and named-function references. It records
// the valid arities of standard functions so that (a) a standard function the
// engine does not implement (doc, collection, …) is still reported as existing,
// and (b) a lookup at an invalid arity (name#2, round#3) correctly yields the
// empty sequence. Functions absent from the catalog fall back to a name-only
// existence check (no arity validation), so omissions never reject a real call.

const arityVariadic = 1 << 30

// fnArityRange maps an fn-namespace local name to its [min,max] arity.
var fnArityRange = map[string][2]int{
	// resolver / environment functions we do not fully implement but that exist
	"error":                           {0, 3},
	"trace":                           {1, 2},
	"doc":                             {1, 1},
	"doc-available":                   {1, 1},
	"collection":                      {0, 1},
	"uri-collection":                  {0, 1},
	"parse-xml":                       {1, 1},
	"parse-xml-fragment":              {1, 1},
	"unparsed-text":                   {1, 2},
	"unparsed-text-lines":             {1, 2},
	"unparsed-text-available":         {1, 2},
	"environment-variable":            {1, 1},
	"available-environment-variables": {0, 0},
	"transform":                       {1, 1},
	"load-xquery-module":              {1, 2},
	"random-number-generator":         {0, 1},
	"idref":                           {1, 2},
	"id":                              {1, 2},
	"element-with-id":                 {1, 2},
	"put":                             {2, 3},
	"function-lookup":                 {2, 2},
	"function-name":                   {1, 1},
	"function-arity":                  {1, 1},
	// The full F&O 3.1 catalog (dispatchFunc enforces these on DIRECT calls
	// too — the 1.0-era engine silently tolerated abs() and abs(1,2)).
	"name":                        {0, 1},
	"local-name":                  {0, 1},
	"namespace-uri":               {0, 1},
	"string":                      {0, 1},
	"data":                        {0, 1},
	"number":                      {0, 1},
	"string-length":               {0, 1},
	"normalize-space":             {0, 1},
	"document-uri":                {0, 1},
	"base-uri":                    {0, 1},
	"static-base-uri":             {0, 0},
	"root":                        {0, 1},
	"abs":                         {1, 1},
	"floor":                       {1, 1},
	"ceiling":                     {1, 1},
	"round":                       {1, 2},
	"round-half-to-even":          {1, 2},
	"substring":                   {2, 3},
	"concat":                      {2, arityVariadic},
	"contains-token":              {2, 3},
	"node-name":                   {0, 1},
	"nilled":                      {0, 1},
	"format-integer":              {2, 3},
	"format-number":               {2, 3},
	"codepoints-to-string":        {1, 1},
	"string-to-codepoints":        {1, 1},
	"compare":                     {2, 3},
	"codepoint-equal":             {2, 2},
	"collation-key":               {1, 2},
	"string-join":                 {1, 2},
	"normalize-unicode":           {1, 2},
	"upper-case":                  {1, 1},
	"lower-case":                  {1, 1},
	"translate":                   {3, 3},
	"contains":                    {2, 3},
	"starts-with":                 {2, 3},
	"ends-with":                   {2, 3},
	"substring-before":            {2, 3},
	"substring-after":             {2, 3},
	"matches":                     {2, 3},
	"replace":                     {3, 4},
	"tokenize":                    {1, 3},
	"analyze-string":              {2, 3},
	"resolve-uri":                 {1, 2},
	"encode-for-uri":              {1, 1},
	"iri-to-uri":                  {1, 1},
	"escape-html-uri":             {1, 1},
	"true":                        {0, 0},
	"false":                       {0, 0},
	"boolean":                     {1, 1},
	"not":                         {1, 1},
	"lang":                        {1, 2},
	"years-from-duration":         {1, 1},
	"months-from-duration":        {1, 1},
	"days-from-duration":          {1, 1},
	"hours-from-duration":         {1, 1},
	"minutes-from-duration":       {1, 1},
	"seconds-from-duration":       {1, 1},
	"dateTime":                    {2, 2},
	"year-from-dateTime":          {1, 1},
	"month-from-dateTime":         {1, 1},
	"day-from-dateTime":           {1, 1},
	"hours-from-dateTime":         {1, 1},
	"minutes-from-dateTime":       {1, 1},
	"seconds-from-dateTime":       {1, 1},
	"timezone-from-dateTime":      {1, 1},
	"year-from-date":              {1, 1},
	"month-from-date":             {1, 1},
	"day-from-date":               {1, 1},
	"timezone-from-date":          {1, 1},
	"hours-from-time":             {1, 1},
	"minutes-from-time":           {1, 1},
	"seconds-from-time":           {1, 1},
	"timezone-from-time":          {1, 1},
	"adjust-dateTime-to-timezone": {1, 2},
	"adjust-date-to-timezone":     {1, 2},
	"adjust-time-to-timezone":     {1, 2},
	"current-date":                {0, 0},
	"current-time":                {0, 0},
	"current-dateTime":            {0, 0},
	"implicit-timezone":           {0, 0},
	"format-dateTime":             {2, 5},
	"format-date":                 {2, 5},
	"format-time":                 {2, 5},
	"parse-ietf-date":             {1, 1},
	"resolve-QName":               {2, 2},
	"QName":                       {2, 2},
	"prefix-from-QName":           {1, 1},
	"local-name-from-QName":       {1, 1},
	"namespace-uri-from-QName":    {1, 1},
	"namespace-uri-for-prefix":    {2, 2},
	"in-scope-prefixes":           {1, 1},
	"path":                        {0, 1},
	"has-children":                {0, 1},
	"innermost":                   {1, 1},
	"outermost":                   {1, 1},
	"generate-id":                 {0, 1},
	"empty":                       {1, 1},
	"exists":                      {1, 1},
	"head":                        {1, 1},
	"tail":                        {1, 1},
	"insert-before":               {3, 3},
	"remove":                      {2, 2},
	"reverse":                     {1, 1},
	"subsequence":                 {2, 3},
	"unordered":                   {1, 1},
	"distinct-values":             {1, 2},
	"index-of":                    {2, 3},
	"deep-equal":                  {2, 3},
	"zero-or-one":                 {1, 1},
	"one-or-more":                 {1, 1},
	"exactly-one":                 {1, 1},
	"count":                       {1, 1},
	"avg":                         {1, 1},
	"max":                         {1, 2},
	"min":                         {1, 2},
	"sum":                         {1, 2},
	"serialize":                   {1, 2},
	"position":                    {0, 0},
	"last":                        {0, 0},
	"default-collation":           {0, 0},
	"default-language":            {0, 0},
	"for-each":                    {2, 2},
	"filter":                      {2, 2},
	"fold-left":                   {3, 3},
	"fold-right":                  {3, 3},
	"for-each-pair":               {3, 3},
	"sort":                        {1, 3},
	"apply":                       {2, 2},
	"parse-json":                  {1, 2},
	"json-doc":                    {1, 2},
	"json-to-xml":                 {1, 2},
	"xml-to-json":                 {1, 2},
}

// mathArityRange maps math-namespace local names to [min,max] arity.
var mathArityRange = map[string][2]int{
	"pi": {0, 0}, "exp": {1, 1}, "exp10": {1, 1}, "log": {1, 1}, "log10": {1, 1},
	"pow": {2, 2}, "sqrt": {1, 1}, "sin": {1, 1}, "cos": {1, 1}, "tan": {1, 1},
	"asin": {1, 1}, "acos": {1, 1}, "atan": {1, 1}, "atan2": {2, 2},
}

// stdArity returns the [min,max] arity range of a standard function and whether
// the function is part of the catalog at all.
func stdArity(ns, local string) (lo, hi int, known bool) {
	switch ns {
	case nsFn:
		r, ok := fnArityRange[local]
		return r[0], r[1], ok
	case nsMath:
		r, ok := mathArityRange[local]
		return r[0], r[1], ok
	case nsXS:
		// Atomic (and built-in list) type constructors take exactly one argument.
		if _, ok := AtomTypeByName(local); ok {
			return 1, 1, true
		}
		if _, ok := xsListItemType(local); ok {
			return 1, 1, true
		}
	}
	return 0, 0, false
}

// fnArityGaps lists arities INSIDE a function's [min,max] range that the
// spec nevertheless does not define: format-date/-time/-dateTime exist only
// as #2 and #5 (format-date-inpt-er4: the 3-argument call is XPST0017).
var fnArityGaps = map[string][]int{
	"format-date":     {3, 4},
	"format-time":     {3, 4},
	"format-dateTime": {3, 4},
}

// stdArityOutside reports whether n falls outside the catalog arities of a
// KNOWN standard function (the [lo,hi] range minus fnArityGaps).
func stdArityOutside(ns, local string, lo, hi, n int) bool {
	if n < lo || (hi != arityVariadic && n > hi) {
		return true
	}
	if ns == nsFn {
		for _, g := range fnArityGaps[local] {
			if g == n {
				return true
			}
		}
	}
	return false
}

// checkStdArity rejects a DIRECT call at an arity the catalog forbids
// (XPST0017) — abs(), abs(1,2), string(1,2) were silently tolerated before.
func checkStdArity(ns, local string, n int) error {
	lo, hi, known := stdArity(ns, local)
	if !known {
		return nil
	}
	if stdArityOutside(ns, local, lo, hi, n) {
		return fmt.Errorf("err:XPST0017: %s() does not accept %d arguments", local, n)
	}
	return nil
}

// mapArityRange maps map-namespace local names (exactly the functions
// registered in mapFuncs) to their [min,max] arity.
var mapArityRange = map[string][2]int{
	"merge": {1, 2}, "size": {1, 1}, "keys": {1, 1}, "contains": {2, 2},
	"get": {2, 2}, "put": {3, 3}, "entry": {2, 2}, "remove": {2, 2},
	"for-each": {2, 2},
}

// arrayArityRange maps array-namespace local names (exactly the functions
// registered in arrayFuncs) to their [min,max] arity.
var arrayArityRange = map[string][2]int{
	"size": {1, 1}, "get": {2, 2}, "append": {2, 2}, "flatten": {1, 1},
	"join": {1, 1}, "for-each": {2, 2},
}

// StdFuncArity reports whether ns/local is a cataloged standard function
// (the fn:/math:/map:/array: namespaces, or an xs: atomic/list-type
// constructor) and, if so, its [lo,hi] arity range. Exported for host
// (XSLT) implementations of fn:function-available.
func StdFuncArity(ns, local string) (lo, hi int, known bool) {
	if lo, hi, ok := stdArity(ns, local); ok {
		return lo, hi, true
	}
	switch ns {
	case nsMap:
		if r, ok := mapArityRange[local]; ok {
			return r[0], r[1], true
		}
	case nsArray:
		if r, ok := arrayArityRange[local]; ok {
			return r[0], r[1], true
		}
	}
	return 0, 0, false
}

// StdArityOK reports whether n is a valid arity for the cataloged standard
// function ns/local (StdFuncArity must already report it known).
func StdArityOK(ns, local string, n int) bool {
	if _, _, known := StdFuncArity(ns, local); !known {
		return false
	}
	lo, hi, _ := StdFuncArity(ns, local)
	return !stdArityOutside(ns, local, lo, hi, n)
}
