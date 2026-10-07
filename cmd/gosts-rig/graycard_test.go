package main

import (
	"os/exec"
	"testing"
)

// The gray card balancing (web/graycard.js) recovers the light's colour
// temperature from synthetic cards, reports a green cast, and refuses
// clipped or dark ones. Run with Node, if it's installed.
func TestGrayCardJS(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node isn't installed")
	}
	out, err := exec.Command(node, "testdata/graycard_test.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
