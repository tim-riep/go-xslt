package xslt

import "sync/atomic"

// The SCHEMA-AWARENESS CLAIM: what this run presents itself as, to the two
// places XSLT 3.0 lets a stylesheet ask — system-property('xsl:is-schema-aware')
// and the XTSE1660 family of "that construct needs a schema-aware processor"
// static errors.
//
// The default is false. xsl:import-schema (decl_import_schema.go) and real
// runtime validation dispatch (schema_validation.go) are both now built and
// gated behind this same switch — a schema-aware run genuinely compiles
// imported components, validates constructed/retrieved nodes against them,
// and annotates real xmltree.Node.SchemaType/TypeAnno values, all inert at
// the default setting. What is NOT built: XPath 3.0 static typing (§26.1,
// out of scope permanently — see the schema-awareness roadmap entry).
//
// The switch exists so the schema-aware PATH could be built and measured one
// piece at a time behind a single gate instead of a scatter of ad-hoc
// conditions — every construct a basic processor must reject asks here. It
// is deliberately a claim and not a capability probe: the only caller today
// is the XSLT30 conformance harness under XSLT30_FULL=1 (which claims this
// alongside streaming — see xsltFullHarness in test/conformance/
// xslt30_test.go), since turning it on for a real caller is a product
// decision (still opt-in, not the default), not something either round made
// unilaterally.
var schemaAware atomic.Bool

// SetSchemaAware sets whether this processor presents itself as schema-aware.
func SetSchemaAware(v bool) { schemaAware.Store(v) }

// schemaAwareRun reports the current claim.
func schemaAwareRun() bool { return schemaAware.Load() }

// validationValueOK reports whether a [xsl:]validation / [xsl:]default-validation
// value is one this run can honour. XSLT 3.0 §3.15 requires a NON-schema-aware
// processor to treat preserve and lax AS IF they were strip — both mean
// "validate however the calling application says", and for such a processor
// that is not at all — so only "strict" genuinely demands typed data, which a
// schema-aware run can supply. Anything outside the four-value vocabulary is
// XTSE1660 either way.
func validationValueOK(v string) bool {
	switch v {
	case "strip", "preserve", "lax":
		return true
	case "strict":
		return schemaAwareRun()
	}
	return false
}
