package git

import (
	"errors"
	"testing"

	"github.com/TheTangentLine/deployctl/internal/steps"
)

const valuesDev = `# Values for dev. Lines marked "managed by deployctl" are edited by it.
backend:
  image:
    repository: registry.example.com/reconcile-backend
    tag: a1b2c3d          # managed by deployctl
  replicaCount: 3         # managed by deployctl
frontend:
  image:
    repository: registry.example.com/reconcile-frontend
    tag: "1.10"           # looks like a number, must stay a string
  replicaCount: 1
weird:
  float: 1.10
  list: [a, b]
  anchor: &shared {x: 1}
  alias: *shared
`

func TestGetYAMLValue(t *testing.T) {
	tests := []struct {
		key     string
		want    string
		wantErr error // nil: success; steps.ErrNotFound; errAny: some other error
	}{
		{key: "backend.image.tag", want: "a1b2c3d"},
		{key: "backend.replicaCount", want: "3"},
		{key: "frontend.image.tag", want: "1.10"},
		{key: "weird.float", want: "1.10"}, // raw text, not the float 1.1

		{key: "backend.image.digest", wantErr: steps.ErrNotFound},
		{key: "nope", wantErr: steps.ErrNotFound},
		{key: "nope.deeper", wantErr: steps.ErrNotFound},

		{key: "backend.image", wantErr: errAny},          // a mapping, not a value
		{key: "weird.list", wantErr: errAny},             // a list, not a value
		{key: "backend.image.tag.more", wantErr: errAny}, // walks into a scalar
		{key: "weird.alias.x", wantErr: errAny},          // aliases not supported
		{key: "backend..tag", wantErr: errAny},           // bad key
		{key: "", wantErr: errAny},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got, err := GetYAMLValue([]byte(valuesDev), tt.key)
			checkErr(t, err, tt.wantErr)
			if tt.wantErr == nil && got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGetYAMLValue_EmptyAndInvalid(t *testing.T) {
	_, err := GetYAMLValue([]byte(""), "backend.image.tag")
	checkErr(t, err, steps.ErrNotFound)

	_, err = GetYAMLValue([]byte("a: [unclosed"), "a")
	checkErr(t, err, errAny)
}

// errAny means "any error, but not ErrNotFound and not retryable".
var errAny = errors.New("any error")

func checkErr(t *testing.T, err, want error) {
	t.Helper()
	switch {
	case want == nil && err != nil:
		t.Fatalf("unexpected error: %v", err)
	case want == nil:
		return
	case err == nil:
		t.Fatalf("expected an error, got nil")
	case want == errAny && errors.Is(err, steps.ErrNotFound):
		t.Fatalf("expected a plain error, got not-found: %v", err)
	case want != errAny && !errors.Is(err, want):
		t.Fatalf("expected %v, got %v", want, err)
	}
	if steps.IsRetryable(err) {
		t.Fatalf("error should not be retryable: %v", err)
	}
}
