# The jacques derivation.
#
# An ordinary buildGoModule since 2026-09-30, and the comment that used to live
# here is worth keeping as history: jacques consumed the shared auth foundation
# through `replace github.com/jack-work/msauth => ../msauth`, so the module
# graph did not close unless msauth sat NEXT TO this repository on disk. No
# vendorHash could rescue that -- a filesystem replace is a path, not a module
# to fetch -- so this file assembled a two-repo WORKSPACE in the store and
# pointed sourceRoot at the jacques half, and flake.nix declared an empty
# placeholder input the consumer was obliged to override.
#
# github.com/jack-work/msauth is public and tagged now, so all of that is gone,
# and so are two hazards that came with it:
#
#   * the vendorHash moved whenever msauth moved, with go.mod and go.sum
#     untouched, because `go mod vendor` copies the replace target into
#     vendor/. On 2026-09-30 that failed a system build deep inside a
#     derivation whose name never mentions msauth.
#   * the link-time `msauth.Stamp` ldflag existed only because a filesystem
#     replace carries no module version. A module-linked artifact records its
#     foundation version in build info; msauth's own auditor calls that "the
#     case the stamp exists to work around".
{
  lib,
  buildGoModule,
  stdenv,

  # Source of this repository.
  jacquesSrc,

  # What `jacques version` reports. See flake.nix for how it is derived.
  version ? "unknown",

  # Kept off by default: see doCheck below.
  runTests ? false,
}:

buildGoModule {
    pname = "jacques";
    inherit version;

    src = jacquesSrc;

    # Covers go.mod and go.sum only, now that no local source is vendored in,
    # so it moves when jacques's dependencies move and not otherwise. It used
    # to move whenever msauth did; see the header.
    vendorHash = "sha256-XATka6kWA9yfKYdc8s4k+4j45XUJzK14vJL21fKW92E=";

    # -trimpath IS TURNED OFF HERE, AND THAT IS THE WHOLE POINT.
    #
    # buildGoModule adds `-trimpath` unless allowGoReference is set, and Go
    # DISCARDS the `-ldflags` build setting whenever -trimpath is on. Measured
    # directly, on a two-line program:
    #
    #   go build          -ldflags "-X main.Stamp=abc1234"  ->  build -ldflags="-X main.Stamp=abc1234"
    #   go build -trimpath -ldflags "-X main.Stamp=abc1234" ->  build -trimpath=true          (no ldflags)
    #
    # The stamp itself is gone now that msauth is a versioned module, but the
    # setting is still load-bearing: msauth's auditor reads an artifact's
    # provenance out of build info precisely so it can judge a binary WITHOUT
    # executing it, and -trimpath is the switch that makes a binary opaque to
    # it. Leave this on. Verified on a real build before this line existed.
    #
    # The cost is honest and was measured too: the untrimmed binary keeps the
    # toolchain's source paths in its pclntab, so nix retains a runtime
    # reference to go, and the closure goes 119MB -> 318MB. That buys back the
    # one property this repository is built around. The marginal cost on a
    # developer box is near zero, since the toolchain is already installed.
    #
    # `jacques version` is NOT the thing at stake: it reads the linked variable
    # at run time and reports `replaced+<rev>` either way. What -trimpath breaks
    # is every check that must judge a binary it will not run.
    allowGoReference = true;

    # NOT CGO_ENABLED = 0.
    #
    # jacques embeds DuckDB through github.com/duckdb/duckdb-go-bindings, which
    # is a cgo binding over a PREBUILT STATIC ARCHIVE vendored per platform
    # (lib/linux-amd64, lib/linux-arm64, lib/darwin-amd64, lib/darwin-arm64,
    # lib/windows-amd64 -- all five are in go.mod). Turning cgo off does not
    # produce a smaller binary, it produces a link failure. That vendored set is
    # also exactly why the platform list in flake.nix is what it is.

    ldflags = [
      "-s"
      "-w"
      "-X"
      "main.buildVersion=${version}"
    ];

    # Off by default, and not because the tests are bad.
    #
    # foundation_gate_test.go judges the jacques binary a USER RUNS, resolved at
    # run time from GOBIN / GOPATH / ~/go/bin. Inside the build sandbox there is
    # no such artifact and no home directory, so it skips -- a skip, not a pass.
    # Running the suite here therefore costs build time and measures nothing that
    # the build has not already proven. Run it where it means something:
    #
    #     nix develop        # then: go test ./...
    #
    # Set runTests = true to exercise the rest of the suite inside the build.
    doCheck = runTests;

    meta = {
      description = "Local Kusto TUI client, query tool and DuckDB/CSV front end";
      homepage = "https://github.com/jack-work/jacques";
      mainProgram = "jacques";
      # The union of the platforms duckdb-go-bindings ships an archive for and
      # the platforms nix builds on. Windows is in that vendored set too and is
      # not a nix platform.
      platforms = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
    };
  }
