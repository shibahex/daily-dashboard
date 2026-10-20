{
  description = "Health dashboard: local server + Taskwarrior/Timewarrior integration";

  inputs.nixpkgs.url = "nixpkgs";

  outputs = { self, nixpkgs }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};

      # Taskwarrior hook: `task start` starts a Timewarrior timer, `stop`/`done` stops it.
      # Absolute store paths so it also works from a plain terminal, outside this flake.
      timewHook = pkgs.writeShellScript "on-modify.timewarrior" ''
        export PATH=${pkgs.lib.makeBinPath [ pkgs.timewarrior pkgs.python3 ]}:$PATH
        exec python3 ${pkgs.timewarrior}/share/doc/timew/ext/on-modify.timewarrior "$@"
      '';

      # The Go server (cmd/dashboard). Its one dependency is goldmark, for Markdown.
      server = pkgs.buildGoModule {
        pname = "health-dashboard-server";
        version = "0.1.0";
        src = ./.;
        vendorHash = "sha256-Xv4CTILTM7Ys4nlB1CKgrQxUJj4LQz23k9v9dWnhHYE=";
        subPackages = [ "cmd/dashboard" ];
      };

      dashboard = pkgs.writeShellApplication {
        name = "health-dashboard";
        # uv runs mfp_fetch.py with the MyFitnessPal MCP package for the MyFitnessPal sync.
        # bubblewrap, socat, bash and coreutils make the AI agents' sandbox (internal/sandbox).
        runtimeInputs = [ pkgs.timewarrior pkgs.uv pkgs.bubblewrap pkgs.socat pkgs.bash pkgs.coreutils pkgs.nodejs_22 pkgs.python312 ];
        text = ''
          # Run from the site folder so the server uses the live files, not a store copy.
          if [ ! -f web/templates/page.html ]; then
            echo "Run this from the site/ folder (cd there, then: nix run)" >&2
            exit 1
          fi
          site="$PWD"

          # The MyFitnessPal MCP's prebuilt curl_cffi wheel needs libstdc++, which Nix's Python lacks.
          export MFP_LIBRARY_PATH=${pkgs.stdenv.cc.cc.lib}/lib

          # Timewarrior asks to create its database on first use; create it up front.
          mkdir -p "''${XDG_DATA_HOME:-$HOME/.local/share}/timewarrior/data" "''${XDG_CONFIG_HOME:-$HOME/.config}/timewarrior"
          touch "''${XDG_CONFIG_HOME:-$HOME/.config}/timewarrior/timewarrior.cfg"

          # Install the hook. The GC root keeps its store path alive after the server exits.
          hooks="$(task _get rc.hooks.location 2>/dev/null || true)"
          hooks="''${hooks:-$HOME/.task/hooks}"
          mkdir -p "$hooks"
          nix-store --add-root "$site/.timew-hook" --realise ${timewHook} > /dev/null
          ln -sfn "$site/.timew-hook" "$hooks/on-modify.timewarrior"

          # A port (default 1628), or `reset-password <username>`.
          if [ "$#" -eq 0 ]; then set -- 1628; fi
          exec ${server}/bin/dashboard "$@"
        '';
      };
    in
    {
      packages.${system} = {
        default = dashboard;
        server = server;
        timew-hook = timewHook;
      };

      apps.${system}.default = {
        type = "app";
        program = "${dashboard}/bin/health-dashboard";
      };

      # `nix develop`: Go, timew and uv, e.g. `go run ./cmd/dashboard 8080` or `timew summary :week`.
      devShells.${system}.default = pkgs.mkShell {
        packages = [ pkgs.go pkgs.gopls pkgs.timewarrior pkgs.uv pkgs.bubblewrap pkgs.socat pkgs.bash pkgs.coreutils pkgs.nodejs_22 pkgs.python312 ];
        MFP_LIBRARY_PATH = "${pkgs.stdenv.cc.cc.lib}/lib";
      };
    };
}
