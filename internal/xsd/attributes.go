package xsd

import (
	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// validateAttributes checks an element's attributes against a complex type's
// (effective) attribute uses and attribute wildcard.
func (s *Schema) validateAttributes(ct *ComplexType, el *xmltree.Node) error {
	uses, wc := s.effectiveAttrs(ct)
	byName := make(map[xname]*attrUse, len(uses))
	for _, u := range uses {
		byName[u.decl.name] = u
	}

	present := map[xname]bool{}
	nID := 0
	for _, a := range el.Attrs {
		an := xname{a.Name.Space, a.Name.Local}
		u, ok := byName[an]
		// xsi:* and xml:* attributes are always permitted, but when one is
		// *explicitly* declared (e.g. ref="xml:base" use="required", or
		// ref="xsi:type" with a fixed value) it must still be validated and
		// counted present — so only skip one that is not declared.
		if !ok && (an.Space == xsiNS || an.Space == xmlNS) {
			// In 1.1 an undeclared xml:* attribute is NOT exempt: it needs a named
			// use or an admitting attribute wildcard — notQName="xml:space", a
			// ##local-only wildcard, or NO wildcard at all must reject it
			// (wild027/045/046/055/056; open044.n1/open045.n1/n2 pin the
			// no-wildcard case). xsi:* stays always-permitted (§3.4.4.2); 1.0
			// keeps the lenient skip.
			if s.version != Version10 && an.Space == xmlNS && (wc == nil || !wildcardMatches(wc, an)) {
				return invalidf("cvc-complex-type.3.2.2", "attribute %s is not allowed", an)
			}
			continue
		}
		if ok && u.prohibited {
			// A prohibited attribute use denotes absence, not an active ban: the
			// attribute is simply not a named use, so it must instead be permitted
			// by the attribute wildcard (XSD 1.0 cvc-complex-type.3.2). Fall through.
			ok = false
		}
		if !ok {
			if wc == nil || !wildcardMatches(wc, an) {
				return invalidf("cvc-complex-type.3.2.2", "attribute %s is not allowed", an)
			}
			// Permitted by the wildcard — but a strict/lax wildcard still has the
			// attribute ASSESSED against its global declaration, which is how an
			// ID arriving through xs:anyAttribute gets counted and bound.
			if wc.process == "skip" {
				continue
			}
			gd, found := s.attributes[an]
			if !found {
				// A strict wildcard demands assessment, but only where we actually
				// hold components for the namespace: matching one we never loaded a
				// schema for is routine and must not be an error (attgD034, ctL021,
				// ns5_1, tns5).
				if wc.process == "strict" {
					return invalidf("cvc-assess-attr", "attribute %s has no declaration to assess it against", an)
				}
				continue
			}
			if gd.typ != nil {
				if err := s.yearZeroOK(gd.typ, a.Value); err != nil {
					return err
				}
				if err := gd.typ.validateIn(a.Value, a); err != nil {
					return err
				}
				if err := s.noteIDValues(gd.typ, a.Value, el, a); err != nil {
					return err
				}
				s.annotateAssertType(a, gd.typ)
				s.noteNodeType(a, gd.typ) // wildcard-assessed: the global declaration governs
				if s.version == Version10 && gd.typ.resolvePrim() == xpath.XSid {
					if nID++; nID > 1 {
						return invalidf("cvc-complex-type.5.1", "an element may carry at most one ID-typed attribute")
					}
				}
			}
			continue
		}
		present[an] = true
		if u.decl.typ != nil {
			if err := s.yearZeroOK(u.decl.typ, a.Value); err != nil {
				return err
			}
			if err := u.decl.typ.validateIn(a.Value, a); err != nil {
				return err
			}
			if err := s.noteIDValues(u.decl.typ, a.Value, el, a); err != nil {
				return err
			}
			s.annotateAssertType(a, u.decl.typ)
			s.noteNodeType(a, u.decl.typ) // a named attribute use governs
			// XSD 1.0 permits at most one ID-typed attribute per element; 1.1
			// dropped the restriction (see noteIDValues, where one element may
			// bind several ID values).
			if s.version == Version10 && u.decl.typ.resolvePrim() == xpath.XSid {
				if nID++; nID > 1 {
					return invalidf("cvc-complex-type.5.1", "an element may carry at most one ID-typed attribute")
				}
			}
		}
		if u.hasFixed {
			// cvc-attribute.4 compares in the governing type's value space —
			// no blanket collapse: a preserve-whiteSpace string keeps its
			// spacing significant (attO008 vs attO007).
			var eq bool
			if u.decl.typ != nil {
				eq = u.decl.typ.fixedEqual(a.Value, u.fixed)
			} else {
				eq = lexEqualCollapsed(a.Value, u.fixed)
			}
			if !eq {
				return invalidf("cvc-attribute.4", "attribute %s must equal the fixed value %q", an, u.fixed)
			}
		}
	}
	for _, u := range uses {
		if u.required && !present[u.decl.name] {
			return invalidf("cvc-complex-type.4", "required attribute %s is missing", u.decl.name)
		}
		// An absent attribute with a value constraint contributes that value to
		// the instance, so an ID default still binds (and an IDREF default still
		// has to resolve).
		if !present[u.decl.name] && !u.prohibited && u.decl.typ != nil {
			switch {
			case u.hasDefault:
				if err := s.noteIDValues(u.decl.typ, u.def, el, el); err != nil {
					return err
				}
				s.noteDefaultAttr(el, u.decl.name, u.def, u.decl.typ)
			case u.hasFixed:
				if err := s.noteIDValues(u.decl.typ, u.fixed, el, el); err != nil {
					return err
				}
				s.noteDefaultAttr(el, u.decl.name, u.fixed, u.decl.typ)
			}
		}
	}
	return nil
}

func lexEqualCollapsed(a, b string) bool {
	return applyWhiteSpace("collapse", a) == applyWhiteSpace("collapse", b)
}
