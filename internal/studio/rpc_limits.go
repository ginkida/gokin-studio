package studio

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	ChatMessageMaxBytes    = 1 << 20
	DispatchTaskMaxBytes   = 256 << 10
	CommitMessageMaxBytes  = 16 << 10
	HistoryQueryMaxBytes   = 4 << 10
	MemoryContentMaxBytes  = 64 << 10
	MemoryEntryIDMaxBytes  = 128
	QuestionAnswerMaxBytes = 64 << 10
	QuestionIDMaxBytes     = 128
	TerminalWriteMaxBytes  = 1 << 20
	TerminalDimensionMax   = 1000
)

func validateRPCText(field, value string, maxBytes int, requireNonBlank bool) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", field)
	}
	if strings.ContainsRune(value, 0) {
		return fmt.Errorf("%s cannot contain NUL bytes", field)
	}
	if requireNonBlank && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s cannot be empty", field)
	}
	if len(value) > maxBytes {
		return fmt.Errorf("%s exceeds the %d-byte limit", field, maxBytes)
	}
	return nil
}
