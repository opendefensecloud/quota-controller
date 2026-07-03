{
  description = "quota-controller - development environment";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";

    go-overlay = {
      url = "github:purpleclay/go-overlay";
      inputs.nixpkgs.follows = "nixpkgs";
      inputs.flake-utils.follows = "flake-utils";
    };

    dev-kit = {
      url = "github:opendefensecloud/dev-kit";
      inputs.nixpkgs.follows = "nixpkgs";
      inputs.go-overlay.follows = "go-overlay";
      inputs.flake-utils.follows = "flake-utils";
    };
  };

  outputs = { nixpkgs, flake-utils, dev-kit, ... }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
      in
      {
        devShells.default = dev-kit.lib.mkShell {
          inherit system;
          goVersion = "1.26.4";
          # dev-kit already provides kind, kubectl, helm, jq, gnumake, yq. Add the
          # Go/Kubernetes toolchain this repo needs: language server, linter, the
          # Task runner, controller-gen, and setup-envtest (integration tests).
          packages = with pkgs; [
            gopls
            golangci-lint
            go-task
            kubernetes-controller-tools
            setup-envtest
          ];
        };
        shellHook = ''
          export IN_NIX_SHELL=1
          # Drop into zsh for an interactive `nix develop`, but NOT when the dev
          # shell is evaluated non-interactively (e.g. direnv's `use flake`, which
          # runs the hook via `nix print-dev-env` and would be hijacked by `exec`).
          if [ -z "$DIRENV_IN_ENVRC" ] && [ -t 1 ]; then
            exec zsh
          fi
        '';
      }
    );
}
