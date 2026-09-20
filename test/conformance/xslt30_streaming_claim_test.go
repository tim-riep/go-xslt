package conformance

import (
	"github.com/tim-riep/go-xslt/internal/xslt"
)

// The harness's default profile presents this engine as a processor WITHOUT the
// XSLT 3.0 streaming feature: xsltUnsupportedFeatures declares it unsupported,
// so the ~2,900 streaming-tagged cases are skipped. system-property(
// 'xsl:supports-streaming') has to make the same claim, or the profile
// contradicts itself — system-property-013 (the feature streaming
// satisfied="false" variant, which runs only in this profile) asserts the
// property says "no", while its complement system-property-012 runs only under
// XSLT30_FULL=1 and asserts "yes".
//
// The engine's own default is "yes", because a real caller does get genuine
// bounded-memory execution for a streamable xsl:source-document (stream_exec.go)
// — this is the harness opting out of a claim it does not make, not the engine
// hiding a capability. It lives in its own file, beside the XSLT30_FULL
// handling in xslt30_test.go's init, purely so this file's own claim-suppression
// logic stays legible on its own rather than buried inside the combined init.
func init() {
	if !xsltFullHarness() {
		xslt.SetStreamingClaim(false)
	}
}
