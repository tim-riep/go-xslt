package xslt

import "sync/atomic"

// The STREAMING FEATURE CLAIM: what system-property('xsl:supports-streaming')
// answers.
//
// This is deliberately NOT strmEnforce. The two used to be the same switch, and
// conflating them got the answer wrong for every real caller: strmEnforce only
// decides whether a streamable="yes" declaration the analysis cannot prove is a
// hard XTSE3430 rather than a silent fall back to full materialization, and
// nothing but the conformance harness ever sets it — while the CAPABILITY it
// was standing in for (xsl:source-document streamable="yes" actually executing
// one record at a time, in memory independent of document size — stream_exec.go)
// is always on. A stylesheet asking the property whether it is worth using
// streaming constructs here was therefore told "no" while the engine was ready
// to stream the very construct it was asking about.
//
// So the default is "yes", and a HOST that is deliberately presenting itself as
// a processor WITHOUT the streaming feature says so with SetStreamingClaim.
// The only such host is the XSLT30 conformance harness in its default profile,
// where the streaming feature is declared unsupported (the ~2,900 streaming
// cases are skipped) and system-property-013 — written for exactly that kind of
// processor — asserts the property agrees.
var strmClaim atomic.Bool

func init() { strmClaim.Store(true) }

// SetStreamingClaim sets what system-property('xsl:supports-streaming')
// reports. A host that does not present itself as offering the XSLT 3.0
// streaming feature sets it to false; everything else leaves the default.
func SetStreamingClaim(v bool) { strmClaim.Store(v) }
