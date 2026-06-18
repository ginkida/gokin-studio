package client

import (
	"errors"
	"strings"
	"testing"
)

func TestEmptyModelResponseError_IsRetryable(t *testing.T) {
	if !IsRetryableError(&EmptyModelResponseError{}) {
		t.Error("EmptyModelResponseError should be retryable")
	}
	if !IsRetryableError(ErrEmptyModelResponse) {
		t.Error("ErrEmptyModelResponse sentinel should be retryable")
	}
}

func TestEmptyModelResponseError_MessageAndUnwrap(t *testing.T) {
	e := &EmptyModelResponseError{}
	if !errors.Is(e, ErrEmptyModelResponse) {
		t.Error("EmptyModelResponseError should unwrap to ErrEmptyModelResponse")
	}
	if e.Error() == "" {
		t.Error("expected a non-empty message")
	}
	after := &EmptyModelResponseError{AfterToolResults: true}
	if after.Error() == e.Error() {
		t.Error("AfterToolResults should change the message")
	}
	if !strings.Contains(after.Error(), "incomplete") {
		t.Errorf("after-tool message should mention incompleteness; got %q", after.Error())
	}
}
