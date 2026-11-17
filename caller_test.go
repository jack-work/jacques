package main

import "testing"

func TestUnitFromCgroup(t *testing.T) {
	cases := map[string]string{
		"0::/user.slice/user-1000.slice/user@1000.service/app.slice/md1-perf.service\n": "md1-perf.service",
		"0::/user.slice/user-1000.slice/user@1000.service/app.slice/run-u42.scope\n":    "run-u42.scope",
		"0::/user.slice/user-1000.slice/session-3.scope\n":                              "",
		"0::/user.slice/user-1000.slice/user@1000.service/init.scope\n":                 "",
		"0::/\n": "",
	}
	for in, want := range cases {
		if got := unitFromCgroup(in); got != want {
			t.Errorf("unitFromCgroup(%q) = %q, want %q", in, got, want)
		}
	}
}
