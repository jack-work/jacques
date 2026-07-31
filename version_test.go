package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// identifiesCode rejects the strings that read like a version but name no
// code. Reporting one of these is a test failure, not a display quirk: an
// operator who asks an installed jacques.exe which auth foundation it links
// must get an answer they can act on.
func identifiesCode(version string) bool {
	switch strings.TrimSpace(version) {
	case "", "(devel)", "unknown":
		return false
	}
	return true
}

// The linked msauth version must always be reported, and must always identify
// code. If this test fails, an installed jacques.exe can no longer be asked
// which auth foundation it contains, which is the condition this field exists
// to prevent.
func TestVersionReportsLinkedMsauthVersion(t *testing.T) {
	report := newVersionReport()
	if !identifiesCode(report.Msauth) {
		t.Fatalf("versionReport.Msauth = %q, which identifies no code; msauth.Version must report a tag, a revision, or the replace stamp", report.Msauth)
	}
	if !identifiesCode(report.Jacques) {
		t.Fatalf("versionReport.Jacques = %q, which identifies no code", report.Jacques)
	}
}

func TestWriteVersionHumanIncludesMsauth(t *testing.T) {
	var out bytes.Buffer
	if err := writeVersion(&out, nil); err != nil {
		t.Fatalf("writeVersion: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "jacques "+buildVersion) {
		t.Errorf("human output missing jacques version: %q", text)
	}
	want := newVersionReport().Msauth
	if !identifiesCode(want) {
		t.Fatalf("reported msauth version %q identifies no code", want)
	}
	if !strings.Contains(text, "msauth") || !strings.Contains(text, want) {
		t.Errorf("human output missing msauth version %q: %q", want, text)
	}
}

func TestWriteVersionJSONIncludesMsauth(t *testing.T) {
	var out bytes.Buffer
	if err := writeVersion(&out, []string{"--json"}); err != nil {
		t.Fatalf("writeVersion --json: %v", err)
	}
	var got versionReport
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", out.String(), err)
	}
	if !identifiesCode(got.Msauth) {
		t.Errorf("json msauth field %q identifies no code: %q", got.Msauth, out.String())
	}
	if !identifiesCode(got.Jacques) {
		t.Errorf("json jacques field %q identifies no code: %q", got.Jacques, out.String())
	}
}
