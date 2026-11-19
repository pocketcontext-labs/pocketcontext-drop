package render

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

// DesiredSchema is nibrunner's desired.schema.json, vendored byte for byte from
// github.com/ilbertt/nibrunner tag v2026.11.12 (commit
// e7986824ed5abdb738364667456b5a24df5f6d62,
// crates/protocol/schema/desired.schema.json, sha256 bc53425dad8e0b3a…). nibrun's
// apps/agent/src/lib/nibrunner/desired.gen.ts is generated from the same file.
// TestVendoredSchemaDigest pins it; docs/render.md says how to update it.
//
//go:embed schema/desired.schema.json
var DesiredSchema []byte

// ValidateSchema checks a desired.json document against DesiredSchema with a
// focused JSON Schema 2020-12 evaluator: exactly the keywords that schema uses
// (local $ref, type, properties, required, additionalProperties, items,
// min/maxItems, minimum, maximum, min/maxLength, pattern, enum, const, anyOf,
// oneOf, not, if/then/else, propertyNames, and the uint32, uint64 and double
// formats). A schema using any other keyword is refused, so a schema bump
// cannot silently weaken the check.
func ValidateSchema(doc []byte) error {
	root, err := loadSchema()
	if err != nil {
		return err
	}
	inst, err := toGeneric(doc)
	if err != nil {
		return fmt.Errorf("document is not JSON: %w", err)
	}
	v := &validator{root: root}
	if errs := v.check(root, inst, "$"); len(errs) > 0 {
		if len(errs) > 5 {
			errs = append(errs[:5], fmt.Sprintf("and %d more", len(errs)-5))
		}
		return fmt.Errorf("desired.json fails nibrunner's schema: %s", strings.Join(errs, "; "))
	}
	return nil
}

var (
	schemaOnce sync.Once
	schemaRoot map[string]any
	schemaErr  error
)

func loadSchema() (map[string]any, error) {
	schemaOnce.Do(func() {
		g, err := toGeneric(DesiredSchema)
		if err != nil {
			schemaErr = fmt.Errorf("vendored schema: %w", err)
			return
		}
		root, ok := g.(map[string]any)
		if !ok {
			schemaErr = fmt.Errorf("vendored schema is not an object")
			return
		}
		if err := checkKeywords(root, "#"); err != nil {
			schemaErr = err
			return
		}
		schemaRoot = root
	})
	return schemaRoot, schemaErr
}

var (
	annotationKeywords = map[string]bool{"$id": true, "$schema": true, "$defs": true, "title": true, "description": true}
	assertionKeywords  = map[string]bool{
		"$ref": true, "type": true, "properties": true, "required": true, "additionalProperties": true,
		"items": true, "minItems": true, "maxItems": true, "minimum": true, "maximum": true,
		"minLength": true, "maxLength": true, "pattern": true, "enum": true, "const": true,
		"anyOf": true, "oneOf": true, "not": true, "if": true, "then": true, "else": true,
		"propertyNames": true, "format": true,
	}
	knownFormats = map[string]bool{"uint32": true, "uint64": true, "double": true}
)

// checkKeywords walks every subschema and refuses unknown keywords, formats
// and patterns the evaluator cannot run.
func checkKeywords(s map[string]any, at string) error {
	for k, val := range s {
		switch {
		case annotationKeywords[k]:
		case assertionKeywords[k]:
		default:
			return fmt.Errorf("vendored schema uses unsupported keyword %q at %s", k, at)
		}
		switch k {
		case "format":
			if f, _ := val.(string); !knownFormats[f] {
				return fmt.Errorf("vendored schema uses unsupported format %v at %s", val, at)
			}
		case "pattern":
			p, _ := val.(string)
			if _, err := compilePattern(p); err != nil {
				return fmt.Errorf("vendored schema pattern at %s: %w", at, err)
			}
		case "$ref":
			if r, _ := val.(string); !strings.HasPrefix(r, "#/$defs/") {
				return fmt.Errorf("vendored schema uses non-local $ref %v at %s", val, at)
			}
		case "properties", "$defs":
			m, _ := val.(map[string]any)
			for name, sub := range m {
				if err := checkSub(sub, at+"/"+k+"/"+name); err != nil {
					return err
				}
			}
		case "anyOf", "oneOf":
			list, _ := val.([]any)
			for i, sub := range list {
				if err := checkSub(sub, fmt.Sprintf("%s/%s/%d", at, k, i)); err != nil {
					return err
				}
			}
		case "items", "not", "if", "then", "else", "propertyNames", "additionalProperties":
			if _, isBool := val.(bool); isBool {
				continue
			}
			if err := checkSub(val, at+"/"+k); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkSub(v any, at string) error {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("vendored schema: %s is not a schema object", at)
	}
	return checkKeywords(m, at)
}

// guestPathPattern is nibrunner's GuestPath and ExecutablePath pattern. RE2 has
// no lookahead, so it runs as matchGuestPath instead.
const guestPathPattern = `^/$|^(/(?!\.\.?(/|$))[^/\\"'\x00-\x1f]+)+$`

type matcher func(string) bool

var (
	patternMu    sync.Mutex
	patternCache = map[string]matcher{}
)

func compilePattern(p string) (matcher, error) {
	patternMu.Lock()
	defer patternMu.Unlock()
	if m, ok := patternCache[p]; ok {
		return m, nil
	}
	var m matcher
	if p == guestPathPattern {
		m = matchGuestPath
	} else {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("pattern %q is not RE2 and has no hand translation: %w", p, err)
		}
		m = re.MatchString
	}
	patternCache[p] = m
	return m, nil
}

// matchGuestPath is "/" or one or more "/segment" where no segment is "." or
// "..", and segments hold no slash, backslash, double or single quote, or
// control character U+0000–U+001F.
func matchGuestPath(s string) bool {
	if s == "/" {
		return true
	}
	if !strings.HasPrefix(s, "/") {
		return false
	}
	for _, seg := range strings.Split(s[1:], "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
		for _, r := range seg {
			if r == '\\' || r == '"' || r == '\'' || r < 0x20 {
				return false
			}
		}
	}
	return true
}

type validator struct {
	root map[string]any
}

func (v *validator) check(s map[string]any, inst any, at string) []string {
	var errs []string
	fail := func(format string, args ...any) {
		errs = append(errs, at+": "+fmt.Sprintf(format, args...))
	}
	if ref, ok := s["$ref"].(string); ok {
		defs, _ := v.root["$defs"].(map[string]any)
		target, ok := defs[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
		if !ok {
			fail("unresolved $ref %s", ref)
		} else {
			errs = append(errs, v.check(target, inst, at)...)
		}
	}
	if t, ok := s["type"]; ok && !typeMatches(t, inst) {
		fail("want type %v, got %s", t, typeName(inst))
		return errs
	}
	if c, ok := s["const"]; ok && !jsonEqual(c, inst) {
		fail("want const %v", c)
	}
	if e, ok := s["enum"].([]any); ok {
		found := false
		for _, x := range e {
			if jsonEqual(x, inst) {
				found = true
				break
			}
		}
		if !found {
			fail("not one of %v", e)
		}
	}
	switch x := inst.(type) {
	case json.Number:
		n, _ := new(big.Rat).SetString(x.String())
		if lim, ok := ratOf(s["minimum"]); ok && n.Cmp(lim) < 0 {
			fail("below minimum %v", s["minimum"])
		}
		if lim, ok := ratOf(s["maximum"]); ok && n.Cmp(lim) > 0 {
			fail("above maximum %v", s["maximum"])
		}
		switch s["format"] {
		case "uint32":
			if !n.IsInt() || n.Sign() < 0 || n.Num().BitLen() > 32 {
				fail("not a uint32")
			}
		case "uint64":
			if !n.IsInt() || n.Sign() < 0 || n.Num().BitLen() > 64 {
				fail("not a uint64")
			}
		}
	case string:
		length := utf8.RuneCountInString(x)
		if lim, ok := ratOf(s["minLength"]); ok && big.NewRat(int64(length), 1).Cmp(lim) < 0 {
			fail("shorter than %v", s["minLength"])
		}
		if lim, ok := ratOf(s["maxLength"]); ok && big.NewRat(int64(length), 1).Cmp(lim) > 0 {
			fail("longer than %v", s["maxLength"])
		}
		if p, ok := s["pattern"].(string); ok {
			m, err := compilePattern(p)
			if err != nil {
				fail("%v", err)
			} else if !m(x) {
				fail("does not match %s", p)
			}
		}
	case []any:
		if lim, ok := ratOf(s["minItems"]); ok && big.NewRat(int64(len(x)), 1).Cmp(lim) < 0 {
			fail("fewer than %v items", s["minItems"])
		}
		if lim, ok := ratOf(s["maxItems"]); ok && big.NewRat(int64(len(x)), 1).Cmp(lim) > 0 {
			fail("more than %v items", s["maxItems"])
		}
		if items, ok := s["items"]; ok {
			for i, el := range x {
				errs = append(errs, v.sub(items, el, fmt.Sprintf("%s[%d]", at, i))...)
			}
		}
	case map[string]any:
		if req, ok := s["required"].([]any); ok {
			for _, r := range req {
				if name, _ := r.(string); name != "" {
					if _, has := x[name]; !has {
						fail("missing required %q", name)
					}
				}
			}
		}
		props, _ := s["properties"].(map[string]any)
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			child := at + "." + k
			if pn, ok := s["propertyNames"]; ok {
				errs = append(errs, v.sub(pn, k, child+" (name)")...)
			}
			if ps, ok := props[k]; ok {
				errs = append(errs, v.sub(ps, x[k], child)...)
			} else if ap, ok := s["additionalProperties"]; ok {
				errs = append(errs, v.sub(ap, x[k], child)...)
			}
		}
	}
	if list, ok := s["anyOf"].([]any); ok {
		matched := false
		for _, sub := range list {
			if len(v.sub(sub, inst, at)) == 0 {
				matched = true
				break
			}
		}
		if !matched {
			fail("matches none of anyOf")
		}
	}
	if list, ok := s["oneOf"].([]any); ok {
		n := 0
		for _, sub := range list {
			if len(v.sub(sub, inst, at)) == 0 {
				n++
			}
		}
		if n != 1 {
			fail("matches %d of oneOf, want exactly 1", n)
		}
	}
	if not, ok := s["not"]; ok && len(v.sub(not, inst, at)) == 0 {
		fail("matches a schema it must not")
	}
	if cond, ok := s["if"]; ok {
		if len(v.sub(cond, inst, at)) == 0 {
			if then, ok := s["then"]; ok {
				errs = append(errs, v.sub(then, inst, at)...)
			}
		} else if els, ok := s["else"]; ok {
			errs = append(errs, v.sub(els, inst, at)...)
		}
	}
	return errs
}

func (v *validator) sub(s any, inst any, at string) []string {
	switch t := s.(type) {
	case bool:
		if t {
			return nil
		}
		return []string{at + ": not allowed"}
	case map[string]any:
		return v.check(t, inst, at)
	}
	return []string{at + ": invalid schema"}
}

func ratOf(v any) (*big.Rat, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return nil, false
	}
	return new(big.Rat).SetString(n.String())
}

func typeName(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	case json.Number:
		if r, ok := new(big.Rat).SetString(x.String()); ok && r.IsInt() {
			return "integer"
		}
		return "number"
	}
	return fmt.Sprintf("%T", v)
}

func typeMatches(t any, inst any) bool {
	one := func(name string) bool {
		got := typeName(inst)
		return got == name || (name == "number" && got == "integer")
	}
	switch x := t.(type) {
	case string:
		return one(x)
	case []any:
		for _, n := range x {
			if s, _ := n.(string); one(s) {
				return true
			}
		}
	}
	return false
}

func jsonEqual(a, b any) bool {
	if an, ok := a.(json.Number); ok {
		bn, ok := b.(json.Number)
		if !ok {
			return false
		}
		ar, ok1 := new(big.Rat).SetString(an.String())
		br, ok2 := new(big.Rat).SetString(bn.String())
		return ok1 && ok2 && ar.Cmp(br) == 0
	}
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && bytes.Equal(ab, bb)
}
