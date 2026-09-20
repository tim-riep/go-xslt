package xslt

import "sync/atomic"

// The BACKWARDS-COMPATIBILITY CLAIM: whether this run implements genuine
// XSLT 1.0 backwards-compatible processing (XSLT 3.0 §3.9.1) — the
// relaxations an [xsl:]version value below 2.0 switches on for its scope:
// xsl:value-of/an AVT's default (unseparated) join takes the STRING VALUE OF
// THE FIRST ITEM instead of joining the whole sequence, arithmetic/general
// comparison/function-argument conversion revert to XPath 1.0's lenient,
// number()/string()/boolean()-based coercions (a multi-item operand takes its
// first item, a non-numeric string converts to NaN rather than erroring, "="
// against a boolean converts both sides to boolean, "<"/">" against
// non-node-set operands always compares numerically), and arithmetic results
// are always xs:double.
//
// The default is false, mirroring schema_property.go's SetSchemaAware: XSLT
// 3.0 treats backwards-compatible mode as an OPTIONAL processor capability
// (like schema-awareness/streaming), not something every conformant
// processor must implement — a processor that does not implement it is
// required only to raise XTDE0160 as a DYNAMIC error when 1.0-requesting
// code is actually reached, never as a static one (see
// checkBackwardsCompatVersion / validateTree's own xsl:version check on a
// literal result element), which is exactly the behaviour every default-mode
// case in this repo's own conformance floor already depends on and which
// stays completely unchanged at the default setting.
//
// The only caller today is the XSLT30 conformance harness under
// XSLT30_FULL=1 (xsltFullHarness in test/conformance/xslt30_test.go),
// alongside streaming and schema-awareness — turning this on for a real
// embedding caller is a product decision, not something this round made
// unilaterally.
var bcCompatClaim atomic.Bool

// SetBackwardsCompatible sets whether this processor presents itself as
// implementing XSLT 1.0 backwards-compatible processing.
func SetBackwardsCompatible(v bool) { bcCompatClaim.Store(v) }

// backwardsCompatRun reports the current claim.
func backwardsCompatRun() bool { return bcCompatClaim.Load() }
