# go-xslt — pure-Go XSLT 3.0 / XPath 3.1 engine (library module).
#
# This module (github.com/tim-riep/go-xslt) is the importable engine; its only
# dependency is golang.org/x/text. The desktop GUI is a separate nested module
# under ./desktop — see desktop/Makefile for `dev` / `build` / `bindings`.

.PHONY: test tidy vet

## test: engine unit tests + conformance harnesses (which skip if suites absent)
test:
	go test ./...

## vet: static analysis over the library
vet:
	go vet ./...

## tidy: tidy the library module
tidy:
	go mod tidy
