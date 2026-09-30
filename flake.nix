# jacques, packaged for nix.
#
# ---------------------------------------------------------------------------
# ONE INPUT, SINCE 2026-09-30
# ---------------------------------------------------------------------------
# github.com/jack-work/msauth is public and tagged, so jacques requires it like
# any other module and this flake needs nothing but nixpkgs:
#
#     nix build github:jack-work/jacques
#
# It used to need a second input. msauth was consumed through a local `replace`
# and the repository was private, so there was no honest default URL for it and
# this flake declared an empty placeholder the consumer had to override. Forget
# the override and the build failed in nix/package.nix. That is all gone.
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

  };

  outputs =
    { self, nixpkgs }:
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
    in
    {
      overlays.default = final: prev: {
        jacques = final.callPackage ./nix/package.nix {
          jacquesSrc = self;
          inherit version;
        };
      };

      packages = forAllSystems (
        { pkgs }:
        rec {
          jacques = pkgs.callPackage ./nix/package.nix {
            jacquesSrc = self;
            inherit version;
          };
          default = jacques;
        }
      );

      devShells = forAllSystems (
        { pkgs }:
        {
          # `nix develop` gives the toolchain, not the package.
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
              echo "[jacques] go $(go version | cut -d' ' -f3)" >&2
            '';
          };
        }
      );
    };
}
