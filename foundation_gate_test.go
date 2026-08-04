package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jack-work/msauth"
)

// jacques links msauth as a Go library, which means it inherits NOTHING from a
// foundation fix until someone rebuilds and reinstalls it. `jacques version`
// already reports the linked foundation, but reporting is not gating: between
// 2026-07-28 and 2026-08-02 the shared foundation was fixed five times and the
// installed artifacts were rebuilt by hand, late, and only because a human
// remembered.
//
// This is the gate. It reads .msauth-floor -- the foundation revision this
// source requires -- and judges the binary a user actually runs. The judgement
// itself lives in msauth: writing a revision comparison, a stamp parser or a
// buildinfo reader here would be a second implementation of the one check whose
// entire value is being identical in every client.
//
// It reads the artifact and never executes it. Running an authenticated tool to
// ask its version can trigger a real credential acquisition, and a tool too
// broken to start is exactly the one whose provenance you most need.

// installedArtifact resolves the jacques binary a user runs, at RUN TIME.
// Nothing here is hardcoded: this repository is public, and an absolute install
// path committed into it would publish an account name. Empty means "no
// installed artifact was found", which is a skip, not a pass.
func installedArtifact() (string, string) {
	if override := os.Getenv("JACQUES_INSTALLED_ARTIFACT"); override != "" {
		return override, "JACQUES_INSTALLED_ARTIFACT"
	}
	name := "jacques"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if binDir := os.Getenv("GOBIN"); binDir != "" {
		return filepath.Join(binDir, name), "GOBIN"
	}
	if goPath := os.Getenv("GOPATH"); goPath != "" {
		return filepath.Join(goPath, "bin", name), "GOPATH/bin"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ""
	}
	return filepath.Join(home, "go", "bin", name), "the default GOPATH"
}

// TestInstalledArtifactCarriesRequiredFoundation fails when the installed
// jacques is older than the foundation this source requires.
//
// A missing artifact SKIPS rather than fails, and the two cases are kept
// strictly apart: "jacques is not installed on this machine" is true for anyone
// who cloned this repository, while "jacques is installed and its auth
// foundation is stale" is the defect. A skip is visible in test output and names
// the variable that makes the gate run; it is not a green tick.
func TestInstalledArtifactCarriesRequiredFoundation(t *testing.T) {
	path, source := installedArtifact()
	if path == "" {
		t.Skip("no installed jacques could be located; set JACQUES_INSTALLED_ARTIFACT to gate one")
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no installed jacques at %s (resolved from %s); set JACQUES_INSTALLED_ARTIFACT to gate a different one", filepath.Base(path), source)
	}

	gate, err := msauth.LoadGate(".", path)
	if err != nil {
		t.Fatalf("loading the foundation gate: %v", err)
	}
	report, err := gate.Audit()
	if err != nil {
		t.Fatalf("auditing %s: %v", filepath.Base(path), err)
	}
	if !report.OK {
		t.Fatal(gate.Explain(report))
	}
	t.Logf("installed jacques (resolved from %s) carries foundation %s, floor %s",
		source, report.Artifacts[0].FoundationVersion, gate.Floor)
}

// TestGateRejectsAPreConvergenceArtifact is the negative control. A gate that
// has never been observed failing is not a gate, and this one guards a property
// that is TRUE on a healthy machine, so without a control its passing says
// nothing. Point JACQUES_STALE_ARTIFACT at any known-old jacques binary.
func TestGateRejectsAPreConvergenceArtifact(t *testing.T) {
	path := os.Getenv("JACQUES_STALE_ARTIFACT")
	if path == "" {
		t.Skip("set JACQUES_STALE_ARTIFACT to a known-old jacques binary to exercise the negative control")
	}
	gate, err := msauth.LoadGate(".", path)
	if err != nil {
		t.Fatalf("loading the foundation gate: %v", err)
	}
	report, err := gate.Audit()
	if err != nil {
		t.Fatalf("auditing the control artifact: %v", err)
	}
	if report.OK {
		t.Fatalf("the gate PASSED a known pre-convergence artifact, so it is not measuring anything: %+v", report.Artifacts[0])
	}
	if gate.Explain(report) == "" {
		t.Error("a failing gate must explain itself and name the remedy")
	}
	t.Logf("control artifact correctly rejected: %s", report.Artifacts[0].Reason)
}

// The floor is only as good as its own readability: a floor file that has gone
// missing or become unparseable must be an error, never a quiet pass.
func TestFloorFileDeclaresARevision(t *testing.T) {
	floor, err := msauth.ReadFloor(".")
	if err != nil {
		t.Fatalf("reading %s: %v", msauth.FloorFile, err)
	}
	if floor == "" {
		t.Fatalf("%s declares no foundation revision", msauth.FloorFile)
	}
}
