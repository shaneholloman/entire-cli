//go:build ignore

// Command normalize rewrites the fetched Core API spec into a form the Go
// client generator (ogen) turns into an ergonomic client. It reads the
// pristine upstream spec (core.openapi.json) and writes a generated,
// committed artifact (core.gen.json) that ogen consumes; the upstream
// file is never mutated, so a refresh is a clean `curl` overwrite.
//
// TEMPORARY: every transform here compensates for a bug in the upstream
// OpenAPI document. The goal is to delete this whole command once the spec
// is fixed at the source (the control-plane service's spec generation) and
// generate ogen straight from core.openapi.json. Each transform below
// names the spec bug it works around so the fix can be matched upstream
// and the transform retired.
//
//  1. Collapse JSON-Schema-2020-12 nullable shorthand — `"type": ["array",
//     "null"]` — to the bare type. ogen models a schema's `type` as a
//     scalar and rejects the union form.
//     Spec fix: emit non-nullable arrays (an absent collection serialises
//     as `[]`, never `null`), i.e. `"type": "array"`. Then this transform
//     finds nothing to change and can be removed.
//
//  2. Collapse each operation's responses to one "2XX" success + one
//     "default" error. The spec only enumerates 200, but creates answer
//     201 and deletes 204; those unenumerated codes route to the error
//     decoder and fail to decode a successful response. The 2XX range
//     matches whatever 2xx the server returns. Folding the 4xx/5xx into a
//     single default (all reference the same ErrorModel, so no shape is
//     lost) also flips ogen into "convenient errors": `(*Success, error)`
//     with any non-2xx as a typed `*ErrorModelStatusCode`.
//     Spec fix: declare each operation's real success code (201 for POST
//     creates, 204 for DELETEs, 200 for reads). With the true code
//     declared, this collapse becomes unnecessary — and dropping it also
//     removes the *…StatusCode wrapper the 2XX range forces, so the
//     generated methods return `(*T, error)` directly again and the
//     command fetch closures no longer unwrap `.Response`.
//
// Run via `go generate ./internal/coreapi/...` (the first generate step in
// gen.go), or by hand after refreshing the spec:
//
//	curl -fsSL https://us.console.entire.io/api/v1/openapi.json \
//	    -o internal/coreapi/spec/core.openapi.json
//	go run spec/normalize.go
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

const (
	srcPath = "spec/core.openapi.json"
	outPath = "spec/core.gen.json"
)

// errorModelRef is the component schema every problem+json error response
// in this spec already points at; the injected default reuses it.
const errorModelRef = "#/components/schemas/ErrorModel"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "normalize: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	raw, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("read spec: %w", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse spec: %w", err)
	}

	collapsed := collapseTypeUnions(doc)
	ops := collapseResponses(doc)

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("encode spec: %w", err)
	}
	if err := os.WriteFile(outPath, buf.Bytes(), 0o644); err != nil { //nolint:gosec // spec is not a secret
		return fmt.Errorf("write spec: %w", err)
	}

	fmt.Printf("normalize: collapsed %d type-union(s), normalized responses on %d operation(s) → %s\n",
		collapsed, ops, outPath)
	return nil
}

// collapseTypeUnions rewrites every schema object whose "type" is a JSON
// array containing "null" (e.g. ["array","null"]) to the bare non-null
// type ("array"). Returns the number of sites rewritten.
func collapseTypeUnions(node any) int {
	switch v := node.(type) {
	case map[string]any:
		count := 0
		if t, ok := v["type"].([]any); ok {
			if scalar, replaced := nonNullType(t); replaced {
				v["type"] = scalar
				count++
			}
		}
		for _, child := range v {
			count += collapseTypeUnions(child)
		}
		return count
	case []any:
		count := 0
		for _, child := range v {
			count += collapseTypeUnions(child)
		}
		return count
	default:
		return 0
	}
}

// nonNullType returns the single non-"null" member of a JSON-Schema type
// union and true when the union has exactly one non-null member (the only
// shape huma emits). Unions with zero or multiple non-null members are
// left untouched so a genuinely polymorphic type isn't silently flattened.
func nonNullType(types []any) (string, bool) {
	var nonNull []string
	for _, t := range types {
		s, ok := t.(string)
		if !ok {
			return "", false
		}
		if s != "null" {
			nonNull = append(nonNull, s)
		}
	}
	if len(nonNull) != 1 || len(types) == len(nonNull) {
		return "", false
	}
	return nonNull[0], true
}

// httpMethods is the set of OpenAPI path-item keys that are operations.
var httpMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"options": true, "head": true, "patch": true, "trace": true,
}

// collapseResponses rewrites each operation's responses to exactly two
// entries: a single "2XX" success and a "default" error.
//
// Collapsing the success to the 2XX range (rather than the literal 200 the
// spec declares) is load-bearing: several endpoints actually answer with
// 201 Created or 204 No Content, codes the spec doesn't enumerate. Under
// the literal-200 form ogen routes those to the error decoder and fails
// with "unexpected Content-Type: application/json". 2XX matches whatever
// 2xx the server sends and decodes it as the success type. The error
// branch likewise folds every declared 4xx/5xx into one default (all
// reference the same ErrorModel, so nothing is lost). Returns the number
// of operations rewritten.
func collapseResponses(doc map[string]any) int {
	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		return 0
	}
	count := 0
	for _, item := range paths {
		pathItem, ok := item.(map[string]any)
		if !ok {
			continue
		}
		for method, op := range pathItem {
			if !httpMethods[method] {
				continue
			}
			operation, ok := op.(map[string]any)
			if !ok {
				continue
			}
			responses, ok := operation["responses"].(map[string]any)
			if !ok {
				continue
			}
			collapsed := map[string]any{"default": defaultErrorResponse()}
			if success := pickSuccessResponse(responses); success != nil {
				collapsed["2XX"] = success
			}
			operation["responses"] = collapsed
			count++
		}
	}
	return count
}

// pickSuccessResponse returns the canonical 2xx response for an operation:
// one that carries a content body if any 2xx does (so a 200-with-body
// isn't shadowed by a bodyless 204), otherwise the first 2xx found. Returns
// nil when the operation declares no 2xx response.
func pickSuccessResponse(responses map[string]any) any {
	var first any
	for status, resp := range responses {
		if len(status) == 0 || status[0] != '2' {
			continue
		}
		if first == nil {
			first = resp
		}
		if m, ok := resp.(map[string]any); ok {
			if content, ok := m["content"].(map[string]any); ok && len(content) > 0 {
				return resp
			}
		}
	}
	return first
}

func defaultErrorResponse() map[string]any {
	return map[string]any{
		"description": "Error",
		"content": map[string]any{
			"application/problem+json": map[string]any{
				"schema": map[string]any{"$ref": errorModelRef},
			},
		},
	}
}
