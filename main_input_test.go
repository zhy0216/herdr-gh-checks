package main

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadMergeChoice(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  mergeChoice
	}{
		{name: "enter", input: "\n", want: mergeChoiceMerge},
		{name: "windows enter", input: "\r\n", want: mergeChoiceMerge},
		{name: "admin", input: "a\n", want: mergeChoiceAdmin},
		{name: "admin uppercase", input: "A\r\n", want: mergeChoiceAdmin},
		{name: "cancel", input: "q\n", want: mergeChoiceCancel},
		{name: "cancel uppercase", input: "Q\n", want: mergeChoiceCancel},
		{name: "escape", input: "\x1b\n", want: mergeChoiceCancel},
		{name: "invalid", input: "merge\n", want: mergeChoiceInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readMergeChoice(strings.NewReader(tt.input))
			if err != nil {
				t.Fatalf("readMergeChoice() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("readMergeChoice() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReadMergeChoiceDistinguishesEOF(t *testing.T) {
	for _, input := range []string{"", "a"} {
		got, err := readMergeChoice(strings.NewReader(input))
		if err != nil {
			t.Fatalf("readMergeChoice(%q) error = %v", input, err)
		}
		if got != mergeChoiceEOF {
			t.Fatalf("readMergeChoice(%q) = %v, want EOF", input, got)
		}
	}
}

func TestReadLineDoesNotConsumeFollowingInput(t *testing.T) {
	r := strings.NewReader("a\nADMIN\n")
	choice, err := readMergeChoice(r)
	if err != nil || choice != mergeChoiceAdmin {
		t.Fatalf("choice = %v, error = %v", choice, err)
	}
	line, err := readLine(r)
	if err != nil {
		t.Fatalf("readLine() error = %v", err)
	}
	if line != "ADMIN" {
		t.Fatalf("readLine() = %q, want ADMIN", line)
	}
}

func TestReadMergeChoiceReturnsReadError(t *testing.T) {
	wantErr := errors.New("boom")
	got, err := readMergeChoice(errorReader{err: wantErr})
	if got != mergeChoiceInvalid || !errors.Is(err, wantErr) {
		t.Fatalf("choice = %v, error = %v, want invalid and %v", got, err, wantErr)
	}
}

type errorReader struct {
	err error
}

func (r errorReader) Read([]byte) (int, error) {
	return 0, r.err
}

var _ io.Reader = errorReader{}
