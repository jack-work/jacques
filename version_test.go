package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// The linked msauth version must always be reported. If this test fails, an
// installed jacques.exe can no longer be asked which auth foundation it
// contains, which is the condition this field exists to prevent.
func TestVersionReportsLinkedMsauthVersion(t *testing.T) {
	report := newVersionReport()
	if strings.TrimSpace(report.Msauth) == "" {
		t.Fatal("versionReport.Msauth is empty; the linked msauth version must always be reported")
	}
	if strings.TrimSpace(report.Jacques) == "" {
		t.Fatal("versionReport.Jacques is empty")
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
	if strings.TrimSpace(got.Msauth) == "" {
		t.Errorf("json output has no msauth field: %q", out.String())
	}
	if strings.TrimSpace(got.Jacques) == "" {
		t.Errorf("json output has no jacques field: %q", out.String())
	}
}
