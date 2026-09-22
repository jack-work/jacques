# The jacques derivation.
#
# ---------------------------------------------------------------------------
# WHY THIS IS NOT A ONE-LINE buildGoModule
# ---------------------------------------------------------------------------
# jacques's go.mod ends with
#
#     replace github.com/jack-work/msauth => ../msauth
#
# so the module graph does not close unless the shared auth foundation sits
# NEXT TO this repository on disk. A derivation whose src is jacques alone
# fails in `go mod download` with a missing replacement directory, and no
# vendorHash can rescue it: the replacement is a filesystem path, not a module
# to fetch.
#
# So the source we hand to buildGoModule is not this repository. It is a
# WORKSPACE built here: a directory holding `jacques/` and `msauth/` as
# siblings, with sourceRoot pointing at the jacques half. That reproduces the
# layout the go.mod already requires, inside the store, hermetically.
#
# `msauthSrc` is supplied by the caller precisely because it cannot be named
# here. github.com/jack-work/msauth does not exist publicly (verified: the API
# returns 404), and this repository IS public, so an absolute path to somebody's
# checkout committed here would publish an account name -- the same reason
# foundation_gate_test.go resolves the installed artifact at run time instead of
# hardcoding it. flake.nix therefore declares the input as an empty placeholder
# and the consumer overrides it; see the error below, which is what you get if
# you forget.
{
  lib,
  buildGoModule,
  runCommand,
  stdenv,

  # Source of github.com/jack-work/msauth, laid out as it is in its own repo
  # (go.mod at the root). Required.
  msauthSrc,

  # Source of this repository.
  jacquesSrc,

  # What `jacques version` reports. See flake.nix for how it is derived.
  version ? "unknown",

  # Short revision of msauthSrc, "+dirty" appended when that worktree carried
  # uncommitted changes. Empty means "do not stamp", and an unstamped build
  # honestly reports `replaced=../msauth` instead of a commit it may not carry.
  msauthStamp ? "",

  # Kept off by default: see doCheck below.
  runTests ? false,
}:

let
  msauthPresent = builtins.pathExists "${msauthSrc}/go.mod";

  workspace = runCommand "jacques-workspace-${version}" { } ''
    mkdir -p $out
    cp -r ${jacquesSrc} $out/jacques
    cp -r ${msauthSrc} $out/msauth
    chmod -R u+w $out
  '';
in

if !msauthPresent then
  throw ''
    jacques cannot be built without the msauth foundation source.

    Its go.mod carries `replace github.com/jack-work/msauth => ../msauth`, so the
    build needs that repository as a sibling checkout. Point the `msauth` input at
    it:

        nix build github:jack-work/jacques --override-input msauth path:/abs/path/to/msauth

    or, from a flake that already has it as an input:

        inputs.jacques.inputs.msauth.follows = "my-msauth-source";

    (got a source with no go.mod at its root: ${msauthSrc})
  ''
else

  buildGoModule {
    pname = "jacques";
    inherit version;

    src = workspace;
    # `workspace` is a store DIRECTORY, so the generic unpackPhase copies it to
    # $NIX_BUILD_TOP/<name-without-hash>. Hence the name has to be repeated here.
    sourceRoot = "jacques-workspace-${version}/jacques";

    # Reset from the "got:" value nix prints when this moves. It moved when the
    # msauth branch landed: the vendor derivation is built from the WORKSPACE,
    # which holds both repos, so a change on either side of the `replace` can
    # shift it even when go.mod and go.sum are untouched.
    vendorHash = "sha256-LdD4tAdIQKyzEDC1vPkvTW+ZKr8o8mHMzaxWOU7MVLU=";

    # -trimpath IS TURNED OFF HERE, AND THAT IS THE WHOLE POINT.
    #
    # buildGoModule adds `-trimpath` unless allowGoReference is set, and Go
    # DISCARDS the `-ldflags` build setting whenever -trimpath is on. Measured
    # directly, on a two-line program:
    #
    #   go build          -ldflags "-X main.Stamp=abc1234"  ->  build -ldflags="-X main.Stamp=abc1234"
    #   go build -trimpath -ldflags "-X main.Stamp=abc1234" ->  build -trimpath=true          (no ldflags)
    #
    # msauth reads the foundation stamp out of exactly that build setting, and
    # only that one (see stampFromBuildInfo: it deliberately reads the artifact
    # without executing it). So a -trimpath build of jacques is
    # VerdictUnstamped -- "the artifact cannot say which foundation code it
    # contains" -- forever. Not stale: UNAUDITABLE, which the foundation treats
    # as worse. Verified on a real build before this line existed.
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
    ]
    ++ lib.optionals (msauthStamp != "") [
      # The foundation is linked as a Go LIBRARY through a local `replace`, so it
      # has no module version for `jacques version` to report and an installed
      # binary can silently carry months-old auth code. Stamping the commit at
      # link time is what makes `jacques version` and `msauth audit` able to
      # answer "does this artifact have that fix". See README.md, "Is the
      # installed binary current".
      "-X"
      "github.com/jack-work/msauth.Stamp=${msauthStamp}"
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
