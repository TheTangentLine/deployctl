package steps

import (
	"errors"
	"fmt"
	"io"
	"testing"
)

func TestRetryable(t *testing.T) {
	if Retryable(nil) != nil {
		t.Fatal("Retryable(nil) should be nil")
	}

	err := fmt.Errorf("push: %w", Retryable(io.ErrUnexpectedEOF))
	if !IsRetryable(err) {
		t.Error("wrapped retryable error not detected")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Error("Retryable should keep the original error reachable with errors.Is")
	}

	notFound := fmt.Errorf("backend.image.tag: %w", ErrNotFound)
	if IsRetryable(notFound) {
		t.Error("ErrNotFound must not be retryable")
	}
	if !errors.Is(notFound, ErrNotFound) {
		t.Error("ErrNotFound not detected")
	}
}
