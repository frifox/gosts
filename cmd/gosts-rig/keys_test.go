package main

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestModelViewerKeys: the model viewer's key list (?, MV_KEYS in the page)
// names every key its handlers take (the model viewer's branch of the
// page's keydown, mveModKey and its MVE_KEYS) and none they don't: a key
// added, changed or taken away there goes in the list too.
func TestModelViewerKeys(t *testing.T) {
	b, err := os.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)
	between := func(from, to string) string {
		i := strings.Index(page, from)
		if i < 0 {
			t.Fatalf("%q not found in the page", from)
		}
		j := strings.Index(page[i:], to)
		if j < 0 {
			t.Fatalf("%q not found after %q", to, from)
		}
		return page[i : i+j]
	}
	// The keys handled.
	handlers := between(`if (!$("modelViewer").hidden) {`, `} else if (!$("viewer").hidden) {`) +
		between("function mveModKey(e) {", "\n}\n")
	handled := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?:e\.key(?:\.toLowerCase\(\))?|\bk) === "([^"]+)"`).FindAllStringSubmatch(handlers, -1) {
		handled[strings.ToLower(m[1])] = true
	}
	for _, m := range regexp.MustCompile(`(\w+): "`).FindAllStringSubmatch(between("const MVE_KEYS = {", "};"), -1) {
		handled[strings.ToLower(m[1])] = true
	}
	// The keys listed.
	listed := map[string]bool{}
	for _, m := range regexp.MustCompile(`k: \[((?:"[^"]*"(?:, )?)*)\]`).FindAllStringSubmatch(between("const MV_KEYS = [", "\n];"), -1) {
		for _, q := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(m[1], -1) {
			listed[q[1]] = true
		}
	}
	if len(handled) == 0 || len(listed) == 0 {
		t.Fatalf("found no keys (handled %v, listed %v): the page's layout changed?", handled, listed)
	}
	var missing, extra []string
	for k := range handled {
		if !listed[k] {
			missing = append(missing, k)
		}
	}
	for k := range listed {
		if !handled[k] {
			extra = append(extra, k)
		}
	}
	slices.Sort(missing)
	slices.Sort(extra)
	if len(missing) > 0 {
		t.Errorf("keys the model viewer takes but its list (MV_KEYS) doesn't name: %q", missing)
	}
	if len(extra) > 0 {
		t.Errorf("keys in the model viewer's list (MV_KEYS) that nothing takes: %q", extra)
	}
}
