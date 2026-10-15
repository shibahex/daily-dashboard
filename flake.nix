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

      dashboard = pkgs.writeShellApplication {
        name = "health-dashboard";
        runtimeInputs = [ pkgs.python3 pkgs.timewarrior ];
        text = ''
          # Run from the site folder so the server uses the live files, not a store copy.
          if [ ! -f server.py ]; then
            echo "Run this from ~/Projects/health/site (cd there, then: nix run)" >&2
            exit 1
          fi
          site="$PWD"

          # Timewarrior asks to create its database on first use; create it up front.
          mkdir -p "''${XDG_DATA_HOME:-$HOME/.local/share}/timewarrior/data" "''${XDG_CONFIG_HOME:-$HOME/.config}/timewarrior"
          touch "''${XDG_CONFIG_HOME:-$HOME/.config}/timewarrior/timewarrior.cfg"

          # Install the hook. The GC root keeps its store path alive after the server exits.
          hooks="$(task _get rc.hooks.location 2>/dev/null || true)"
          hooks="''${hooks:-$HOME/.task/hooks}"
          mkdir -p "$hooks"
          nix-store --add-root "$site/.timew-hook" --realise ${timewHook} > /dev/null
          ln -sfn "$site/.timew-hook" "$hooks/on-modify.timewarrior"

          exec python3 server.py "''${1:-1628}"
        '';
      };
    in
    {
      packages.${system} = {
        default = dashboard;
        timew-hook = timewHook;
      };

      apps.${system}.default = {
        type = "app";
        program = "${dashboard}/bin/health-dashboard";
      };

      # `nix develop` gives you timew/python3 in a shell, e.g. for `timew summary :week`.
      devShells.${system}.default = pkgs.mkShell {
        packages = [ pkgs.python3 pkgs.timewarrior ];
      };
    };
}
