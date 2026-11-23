package git

import (
	"strings"
	"testing"

	"github.com/TheTangentLine/deployctl/internal/steps"
)

func TestSetYAMLValue(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		key   string
		value string
		kind  steps.ValueKind
		want  string // full expected output
	}{
		{
			name:  "plain tag, aligned comment kept",
			in:    "backend:\n  image:\n    tag: a1b2c3d          # managed by deployctl\n  replicaCount: 3\n",
			key:   "backend.image.tag",
			value: "e4f5a6b",
			want:  "backend:\n  image:\n    tag: e4f5a6b          # managed by deployctl\n  replicaCount: 3\n",
		},
		{
			name:  "double-quoted tag stays double-quoted",
			in:    "image:\n  tag: \"4c0362c\"\n",
			key:   "image.tag",
			value: "e4f5a6b",
			want:  "image:\n  tag: \"e4f5a6b\"\n",
		},
		{
			name:  "single-quoted tag stays single-quoted",
			in:    "image:\n  tag: 'a1b2c3d'\n",
			key:   "image.tag",
			value: "e4f5a6b",
			want:  "image:\n  tag: 'e4f5a6b'\n",
		},
		{
			name:  "numeric-looking tag gets quoted",
			in:    "image:\n  tag: a1b2c3d\n",
			key:   "image.tag",
			value: "1.10",
			want:  "image:\n  tag: \"1.10\"\n",
		},
		{
			name:  "bool-looking tag gets quoted",
			in:    "image:\n  tag: a1b2c3d\n",
			key:   "image.tag",
			value: "true",
			want:  "image:\n  tag: \"true\"\n",
		},
		{
			name:  "replica count",
			in:    "backend:\n  replicaCount: 3   # managed\n",
			key:   "backend.replicaCount",
			value: "5",
			kind:  steps.ValueInt,
			want:  "backend:\n  replicaCount: 5   # managed\n",
		},
		{
			name:  "same key name elsewhere is untouched",
			in:    "backend:\n  image:\n    tag: aaa\nfrontend:\n  image:\n    tag: aaa\n",
			key:   "frontend.image.tag",
			value: "bbb",
			want:  "backend:\n  image:\n    tag: aaa\nfrontend:\n  image:\n    tag: bbb\n",
		},
		{
			name:  "flow mapping",
			in:    "image: {repository: r, tag: a1b2c3d}\n",
			key:   "image.tag",
			value: "e4f5a6b",
			want:  "image: {repository: r, tag: e4f5a6b}\n",
		},
		{
			name:  "CRLF line endings",
			in:    "image:\r\n  tag: a1b2c3d\r\n  pull: Always\r\n",
			key:   "image.tag",
			value: "e4f5a6b",
			want:  "image:\r\n  tag: e4f5a6b\r\n  pull: Always\r\n",
		},
		{
			name:  "non-ASCII earlier on the line",
			in:    "ø: {tag: a1b2c3d}\n",
			key:   "ø.tag",
			value: "e4f5a6b",
			want:  "ø: {tag: e4f5a6b}\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed, err := SetYAMLValue([]byte(tt.in), tt.key, tt.value, tt.kind)
			if err != nil {
				t.Fatal(err)
			}
			if !changed {
				t.Fatal("changed = false")
			}
			if string(got) != tt.want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

// The real values file from the docs: the edit must change exactly one line.
func TestSetYAMLValue_OneLineDiff(t *testing.T) {
	got, changed, err := SetYAMLValue([]byte(valuesDev), "backend.image.tag", "e4f5a6b", steps.ValueString)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	before := strings.Split(valuesDev, "\n")
	after := strings.Split(string(got), "\n")
	if len(before) != len(after) {
		t.Fatalf("line count changed: %d -> %d", len(before), len(after))
	}
	var diff []string
	for i := range before {
		if before[i] != after[i] {
			diff = append(diff, before[i]+"  ->  "+after[i])
		}
	}
	if len(diff) != 1 {
		t.Fatalf("expected exactly one changed line, got %d:\n%s", len(diff), strings.Join(diff, "\n"))
	}
	if !strings.Contains(diff[0], "tag: e4f5a6b          # managed by deployctl") {
		t.Fatalf("unexpected change: %s", diff[0])
	}
}

func TestSetYAMLValue_NoChange(t *testing.T) {
	in := []byte("image:\n  tag: a1b2c3d\n")
	got, changed, err := SetYAMLValue(in, "image.tag", "a1b2c3d", steps.ValueString)
	if err != nil || changed || string(got) != string(in) {
		t.Fatalf("got changed=%v err=%v content=%q", changed, err, got)
	}
}

func TestSetYAMLValue_Errors(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		key   string
		value string
		kind  steps.ValueKind
		want  error // steps.ErrNotFound or errAny
	}{
		{"missing key", "image:\n  tag: a\n", "image.digest", "x", steps.ValueString, steps.ErrNotFound},
		{"not a value", "image:\n  tag: a\n", "image", "x", steps.ValueString, errAny},
		{"null value", "image:\n  tag:\n", "image.tag", "x", steps.ValueString, errAny},
		{"block scalar", "image:\n  tag: |\n    a\n", "image.tag", "x", steps.ValueString, errAny},
		{"explicit tag", "image:\n  tag: !!str a\n", "image.tag", "x", steps.ValueString, errAny},
		{"int kind, not a number", "replicas: 3\n", "replicas", "five", steps.ValueInt, errAny},
		{"empty value", "image:\n  tag: a\n", "image.tag", "", steps.ValueString, errAny},
		{"newline in value", "image:\n  tag: a\n", "image.tag", "a\nb: c", steps.ValueString, errAny},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := SetYAMLValue([]byte(tt.in), tt.key, tt.value, tt.kind)
			checkErr(t, err, tt.want)
		})
	}
}
