package main

import (
	"github.com/tim-riep/go-xslt/engine"
)

// XsltService is the Wails service exposed to the frontend for running the
// engine. Its exported methods are bound to TypeScript by the Wails binding
// generator. Project/file management lives in ProjectService.
type XsltService struct{}

// RunTransform compiles and executes an XSLT transformation and returns the
// serialized output together with any diagnostics.
func (s *XsltService) RunTransform(req engine.Request) engine.Result {
	return engine.Transform(req)
}

// RunValidate validates an XML instance against one or more XSD schemas
// (version "1.0" or "1.1") and returns the validity plus any diagnostics.
func (s *XsltService) RunValidate(req engine.ValidateRequest) engine.ValidateResult {
	return engine.Validate(req)
}

// RunXPath evaluates a standalone XPath 3.1 expression, optionally against a
// context XML document (req.Source may be empty for expressions needing no
// input, e.g. "1 + 2" or "current-dateTime()").
func (s *XsltService) RunXPath(req engine.XPathRequest) engine.XPathResult {
	return engine.EvalXPath(req)
}
