package render

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Canonical JSON: object keys sorted bytewise, numbers kept as written, no HTML
// escaping. The document file is indented with two spaces and ends in a
// newline; hashing uses the compact form.

func toGeneric(v any) (any, error) {
	var data []byte
	switch t := v.(type) {
	case []byte:
		data = t
	default:
		var err error
		if data, err = json.Marshal(v); err != nil {
			return nil, err
		}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var g any
	if err := dec.Decode(&g); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing data after the JSON value")
	}
	return g, nil
}

func encode(g any, indent bool) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent {
		enc.SetIndent("", "  ")
	}
	// encoding/json writes map keys sorted.
	if err := enc.Encode(g); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func canonicalIndent(v any) ([]byte, error) {
	g, err := toGeneric(v)
	if err != nil {
		return nil, err
	}
	return encode(g, true)
}

func contentHashOf(v any) (string, error) {
	g, err := toGeneric(v)
	if err != nil {
		return "", err
	}
	obj, ok := g.(map[string]any)
	if !ok {
		return "", fmt.Errorf("desired document is not a JSON object")
	}
	delete(obj, "revision")
	data, err := encode(obj, false)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(bytes.TrimSuffix(data, []byte("\n")))
	return hex.EncodeToString(sum[:]), nil
}

// ContentHash returns the hex sha256 of a desired.json document's compact
// canonical JSON without its revision. Equal hashes mean the documents differ
// at most in revision and formatting, so the agent can skip the write.
func ContentHash(doc []byte) (string, error) {
	return contentHashOf(doc)
}
