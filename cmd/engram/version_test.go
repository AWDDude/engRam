package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func TestVersionIsSemver(t *testing.T) {
	// A release build overrides this via ldflags, but the constant is what
	// source builds report — an empty or malformed value would ship silently.
	if !regexp.MustCompile(`^\d+\.\d+\.\d+`).MatchString(version) {
		t.Errorf("version = %q, want a semver-looking string", version)
	}
}

func TestRunVersionIncludesVersionAndPlatform(t *testing.T) {
	var buf bytes.Buffer
	runVersion(&buf)
	out := buf.String()

	if !strings.Contains(out, version) {
		t.Errorf("version output %q does not contain the version %q", out, version)
	}
	if !strings.HasPrefix(out, "engram ") {
		t.Errorf("version output should name the binary first, got %q", out)
	}
	// The platform and toolchain are there to make bug reports self-describing.
	if !strings.Contains(out, "go1") {
		t.Errorf("version output %q does not mention the Go version", out)
	}
}

func TestUsageListsEveryCommand(t *testing.T) {
	// Guards against adding a subcommand to main's dispatch and leaving it
	// undiscoverable in the help text.
	for _, cmd := range commands {
		if !strings.Contains(usageText, "engram "+cmd) {
			t.Errorf("usage text does not document the %q command", cmd)
		}
	}
}

func TestRunUsageWritesToTheGivenWriter(t *testing.T) {
	// main sends usage to stderr when rejecting a bad flag and to stdout when
	// asked for it directly, so it must not hardcode a destination.
	var buf bytes.Buffer
	runUsage(&buf)
	if !strings.Contains(buf.String(), "Usage:") {
		t.Errorf("usage output missing, got %q", buf.String())
	}
}
