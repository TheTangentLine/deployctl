package git

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"github.com/TheTangentLine/deployctl/internal/steps"
)

// SetYAMLValue sets the scalar at a dotted key path to value and returns
// the new document. changed is false (and content is returned as is) when
// the value is already set.
//
// It does not re-encode the document. It uses the YAML parser only to find
// where the value sits in the file, then replaces exactly that text. So
// comments, alignment, indentation and key order are untouched, and the
// diff is one line.
//
// kind decides how the value is written: steps.ValueString keeps it a
// string (an image tag like 1.10 is quoted so it does not become a
// number), steps.ValueInt writes a plain integer.
//
// A missing key is an error (wrapping steps.ErrNotFound): this function
// changes values, it never creates structure.
func SetYAMLValue(content []byte, key, value string, kind steps.ValueKind) ([]byte, bool, error) {
	path, err := splitKey(key)
	if err != nil {
		return nil, false, err
	}
	if err := checkValue(value, kind); err != nil {
		return nil, false, fmt.Errorf("%s: %w", key, err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return nil, false, fmt.Errorf("parse yaml: %w", err)
	}
	n, err := findNode(&doc, path)
	if err != nil {
		return nil, false, err
	}
	if n.Kind != yaml.ScalarNode {
		return nil, false, fmt.Errorf("%s is not a single value", key)
	}
	if n.ShortTag() == "!!null" {
		return nil, false, fmt.Errorf("%s has no value to replace", key)
	}
	if n.Value == value {
		return content, false, nil
	}

	start, end, err := scalarSpan(content, n)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", key, err)
	}
	rendered, err := renderScalar(value, kind, n.Style)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", key, err)
	}

	out := make([]byte, 0, len(content)-(end-start)+len(rendered))
	out = append(out, content[:start]...)
	out = append(out, rendered...)
	out = append(out, content[end:]...)

	// Never hand back a file we cannot read back correctly.
	if err := verify(out, path, value, kind); err != nil {
		return nil, false, fmt.Errorf("%s: edit did not round-trip, refusing it: %w", key, err)
	}
	return out, true, nil
}

func checkValue(value string, kind steps.ValueKind) error {
	switch kind {
	case steps.ValueString:
		if value == "" {
			return errors.New("empty value")
		}
		if strings.ContainsAny(value, "\n\r") {
			return errors.New("value must be a single line")
		}
	case steps.ValueInt:
		if _, err := strconv.Atoi(value); err != nil {
			return fmt.Errorf("%q is not an integer", value)
		}
	default:
		return fmt.Errorf("unknown value kind %d", kind)
	}
	return nil
}

// scalarSpan returns the byte range of the scalar's text in content,
// including quotes if it is quoted.
func scalarSpan(content []byte, n *yaml.Node) (start, end int, err error) {
	if n.Style&(yaml.LiteralStyle|yaml.FoldedStyle|yaml.TaggedStyle) != 0 {
		return 0, 0, errors.New("block scalars and explicit tags are not supported")
	}

	// yaml.v3 positions are 1-based lines and 1-based character columns.
	start = 0
	for line := 1; line < n.Line; line++ {
		i := bytes.IndexByte(content[start:], '\n')
		if i < 0 {
			return 0, 0, errors.New("value position is outside the file")
		}
		start += i + 1
	}
	for col := 1; col < n.Column; col++ {
		r, size := utf8.DecodeRune(content[start:])
		if size == 0 || r == '\n' {
			return 0, 0, errors.New("value position is outside its line")
		}
		start += size
	}

	switch {
	case n.Style&yaml.DoubleQuotedStyle != 0:
		end, err = quotedEnd(content, start, '"')
	case n.Style&yaml.SingleQuotedStyle != 0:
		end, err = quotedEnd(content, start, '\'')
	default: // plain: the source text is exactly the value
		if strings.Contains(n.Value, "\n") || !bytes.HasPrefix(content[start:], []byte(n.Value)) {
			return 0, 0, errors.New("multi-line values are not supported")
		}
		end = start + len(n.Value)
	}
	return start, end, err
}

// quotedEnd returns the index just past the closing quote of a quoted
// scalar that starts at start. Quoted values must be on one line.
func quotedEnd(content []byte, start int, quote byte) (int, error) {
	if start >= len(content) || content[start] != quote {
		return 0, errors.New("expected a quoted value")
	}
	for i := start + 1; i < len(content); i++ {
		switch c := content[i]; {
		case c == '\n':
			return 0, errors.New("multi-line values are not supported")
		case quote == '"' && c == '\\':
			i++ // skip the escaped character
		case c == quote && quote == '\'' && i+1 < len(content) && content[i+1] == '\'':
			i++ // '' is an escaped single quote
		case c == quote:
			return i + 1, nil
		}
	}
	return 0, errors.New("unterminated quoted value")
}

// renderScalar writes value as YAML text, keeping the original quoting
// style where it can.
func renderScalar(value string, kind steps.ValueKind, style yaml.Style) (string, error) {
	if kind == steps.ValueInt {
		return value, nil
	}
	switch {
	case style&yaml.SingleQuotedStyle != 0:
		return "'" + strings.ReplaceAll(value, "'", "''") + "'", nil
	case style&yaml.DoubleQuotedStyle != 0:
		return doubleQuote(value)
	case plainIsString(value):
		return value, nil
	default:
		// Plain would read back as a number, bool, null, ... (1.10, true,
		// 0x1F) or would break the line (": ", " #"). Quote it.
		return doubleQuote(value)
	}
}

func doubleQuote(value string) (string, error) {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range value {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			return "", fmt.Errorf("value contains a control character")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String(), nil
}

// plainIsString reports whether value, written without quotes, reads
// back as exactly the same string.
func plainIsString(value string) bool {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte("k: "+value+"\n"), &doc); err != nil {
		return false
	}
	m := doc.Content[0]
	if m.Kind != yaml.MappingNode || len(m.Content) != 2 {
		return false
	}
	v := m.Content[1]
	return v.Kind == yaml.ScalarNode && v.ShortTag() == "!!str" && v.Value == value
}

// verify re-parses the edited document and checks the value and its type.
func verify(content []byte, path []string, value string, kind steps.ValueKind) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return err
	}
	n, err := findNode(&doc, path)
	if err != nil {
		return err
	}
	if n.Value != value {
		return fmt.Errorf("reads back as %q", n.Value)
	}
	want := map[steps.ValueKind]string{steps.ValueString: "!!str", steps.ValueInt: "!!int"}[kind]
	if n.ShortTag() != want {
		return fmt.Errorf("reads back as %s, want %s", n.ShortTag(), want)
	}
	return nil
}
