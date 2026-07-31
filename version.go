package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/jack-work/msauth"
)

// versionReport is the answer to `jacques version`.
//
// The msauth field is not decoration. jacques links the shared msauth
// foundation as a Go library, so a fix made in msauth reaches users only when
// jacques is rebuilt and reinstalled; an installed jacques.exe can silently
// carry auth code that is months behind the foundation. Reporting the linked
// module version turns "does this artifact have that fix" from artifact
// archaeology into one command. msauth.Version() derives it from Go build
// info, so it needs no network, no identity, and no token cache, and `jacques
// version` remains safe to run anywhere.
type versionReport struct {
	Jacques string `json:"jacques"`
	Msauth  string `json:"msauth"`
}

func newVersionReport() versionReport {
	return versionReport{
		Jacques: buildVersion,
		Msauth:  msauth.Version(),
	}
}

// writeVersion renders the report. `--json` mirrors `config list --json`.
func writeVersion(w io.Writer, args []string) error {
	report := newVersionReport()
	if len(args) > 0 && args[0] == "--json" {
		b, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(b))
		return err
	}
	_, err := fmt.Fprintf(w, "jacques %s\nmsauth  %s\n", report.Jacques, report.Msauth)
	return err
}
