package server

import (
	"strings"
	"testing"
)

func TestSearchLimitDescription_NamesTheConfiguredDefault(t *testing.T) {
	// The description is built at registration time so it can state the real
	// configured cap rather than an abstract "the default".
	got := searchLimitDescription(20)
	if !strings.Contains(got, "(20)") {
		t.Errorf("expected the configured default to appear in the description, got: %s", got)
	}

	if other := searchLimitDescription(5); !strings.Contains(other, "(5)") {
		t.Errorf("description did not track a different configured default, got: %s", other)
	}
}

func TestSearchLimitDescription_UncappedDefaultReadsSensibly(t *testing.T) {
	// default_limit 0 is legal and means uncapped. Interpolating it directly
	// would render "the configured default (0)", which reads as "returns
	// nothing" — the opposite of what it does.
	got := searchLimitDescription(0)
	if strings.Contains(got, "default (0)") {
		t.Errorf("an uncapped default must not render as \"(0)\", got: %s", got)
	}
	if !strings.Contains(got, "no cap") {
		t.Errorf("expected an uncapped default to be described as such, got: %s", got)
	}
}

func TestSearchLimitDescription_StatesTheInvertedConvention(t *testing.T) {
	// Omitting limit caps results while 0 removes the cap — the reverse of the
	// usual "leave it blank for everything". Callers get this backwards unless
	// it is said outright.
	got := strings.ToLower(searchLimitDescription(20))
	for _, want := range []string{"omit", "0", "cap"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected the description to mention %q, got: %s", want, got)
		}
	}
	if !strings.Contains(got, "fewer results") {
		t.Errorf("expected the description to explain short result lists, got: %s", got)
	}
}
