# jacques, packaged for nix.
#
# ---------------------------------------------------------------------------
# THE ONE THING TO KNOW: THE msauth INPUT
# ---------------------------------------------------------------------------
# jacques links github.com/jack-work/msauth through a local `replace` in go.mod,
# and that repository is not public (github.com/jack-work/msauth returns 404).
# There is therefore no honest default URL for it, and this repository is public,
# so a path into somebody's home directory must not be committed here.
#
# The input below is a deliberate PLACEHOLDER -- an empty directory that exists
# on every unix box, so the lock file resolves and nothing about the author's
# machine is published. Building requires overriding it:
#
#     nix build github:jack-work/jacques --override-input msauth path:/abs/path/to/msauth
#
# or, from a consuming flake:
#
#     inputs.jacques.url = "github:jack-work/jacques";
#     inputs.jacques.inputs.msauth.follows = "msauth";   # your own source input
#
# Forget it and you get nix/package.nix's error, which names the remedy. This is
# not a nix quirk: a plain `go build` in a checkout without the sibling fails the
# same way, for the same reason.
#
# ---------------------------------------------------------------------------
# PLATFORMS
# ---------------------------------------------------------------------------
# Four: {x86_64,aarch64} x {linux,darwin}. That is not a wish list, it is the
# set duckdb-go-bindings vendors a static archive for, minus windows (which is
# in that set and is not a nix platform). cgo stays ON for the same reason --
# see nix/package.nix.
{
  description = "jacques -- a local Kusto TUI client, query tool and DuckDB/CSV front end";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

    # See the header. Override this; do not build against it.
    msauth = {
      url = "path:/var/empty";
      flake = false;
    };
  };

  outputs =
    { self, nixpkgs, msauth }:
    let
      supportedSystems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      forAllSystems =
        f:
        nixpkgs.lib.genAttrs supportedSystems (system: f { pkgs = nixpkgs.legacyPackages.${system}; });

      # What `jacques version` reports.
      #
      # There are no tags in this repository yet, so a semver here would be a
      # number nothing mints. The revision is the thing an artifact can actually
      # be audited against, and `+dirty` is carried for the same reason the
      # msauth stamp carries it: a build that includes uncommitted work must not
      # name a clean commit whose content it does not have.
      version =
        if self ? rev then
          "git-${builtins.substring 0 7 self.rev}"
        else if self ? dirtyRev then
          "git-${builtins.substring 0 7 self.dirtyRev}+dirty"
        else
          "unknown";

      # The link-time foundation stamp, i.e. README.md's shell recipe expressed
      # against the locked input rather than against whatever `git -C ../msauth`
      # happens to say at build time. Nix's dirtyShortRev is "<7hex>-dirty";
      # msauth's convention is "<7hex>+dirty".
      msauthStamp =
        if msauth ? shortRev then
          msauth.shortRev
        else if msauth ? dirtyShortRev then
          "${builtins.substring 0 7 msauth.dirtyShortRev}+dirty"
        else
          "";
    in
    {
      overlays.default = final: prev: {
        jacques = final.callPackage ./nix/package.nix {
          msauthSrc = msauth;
          jacquesSrc = self;
          inherit version msauthStamp;
        };
      };

      packages = forAllSystems (
        { pkgs }:
        rec {
          jacques = pkgs.callPackage ./nix/package.nix {
            msauthSrc = msauth;
            jacquesSrc = self;
            inherit version msauthStamp;
          };
          default = jacques;
        }
      );

      devShells = forAllSystems (
        { pkgs }:
        {
          # `nix develop` gives the toolchain, not the package -- entering a
          # shell must not depend on the msauth override being present, since
          # fixing that layout is one of the things you enter a shell to do.
          #
          # azure-cli is here because it is not optional at RUN time: msauth's
          # non-windows build has no broker and no device-code path on this
          # tenant, so `az account get-access-token` is the only token source,
          # and msauth shells out to it by name.
          default = pkgs.mkShell {
            name = "jacques-dev";
            packages = with pkgs; [
              go
              gopls
              gotools
              azure-cli
            ];
            shellHook = ''
              echo "[jacques] go $(go version | cut -d' ' -f3); msauth expected at ../msauth" >&2
            '';
          };
        }
      );
    };
}
