{
  description = "Health dashboard: local server + Taskwarrior/Timewarrior integration";

  inputs.nixpkgs.url = "nixpkgs";

  outputs = { self, nixpkgs }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
      # Claude Code is unfree; allow just that one package.
      unfreePkgs = import nixpkgs {
        inherit system;
        config.allowUnfreePredicate = p: pkgs.lib.getName p == "claude-code";
      };

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
        vendorHash = "sha256-ZAeN2uupjG5trOKZs69/FazUXWYSLq7I7vFTl1uGcJM=";
        subPackages = [ "cmd/dashboard" ];
      };

      dashboard = pkgs.writeShellApplication {
        name = "health-dashboard";
        # uv runs the MyFitnessPal MCP (connecting, renewing its login, and the AI's tools).
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
      # What the container (../Dockerfile) runs: the server with every program it needs, since
      # the image has nothing else. The site's pages sit at /app/site and its data on a volume.
      container = pkgs.writeShellApplication {
        name = "health-dashboard-container";
        # The image has no Nix to fetch the AIs on the server later, so they come built in.
        runtimeInputs = [ pkgs.taskwarrior3 pkgs.timewarrior pkgs.uv pkgs.bubblewrap pkgs.socat pkgs.bash pkgs.coreutils pkgs.nodejs_22 pkgs.python312
          unfreePkgs.claude-code pkgs.codex pkgs.gemini-cli ];
        text = ''
          export MFP_LIBRARY_PATH=${pkgs.stdenv.cc.cc.lib}/lib
          export SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt
          export TZDIR=${pkgs.tzdata}/share/zoneinfo ZONEINFO=${pkgs.tzdata}/share/zoneinfo
          mkdir -p "$HOME" /tmp

          # The first admin uses "this computer's" Taskwarrior and Timewarrior, which in the
          # container live in $HOME on the volume; nothing else sets them up.
          if [ ! -e "$HOME/.taskrc" ]; then
            printf '%s\n' "# Made by the container for the admin's own tasks." \
              "data.location=$HOME/.task" "hooks.location=$HOME/.task/hooks" "hooks=on" > "$HOME/.taskrc"
          fi
          mkdir -p "$HOME/.task/hooks" "$HOME/.local/share/timewarrior/data" "$HOME/.config/timewarrior"
          touch "$HOME/.config/timewarrior/timewarrior.cfg"
          ln -sfn ${timewHook} "$HOME/.task/hooks/on-modify.timewarrior"

          exec ${server}/bin/dashboard "$@"
        '';
      };

      # The image GitHub publishes (.github/workflows/docker.yml), the same as ../Dockerfile makes
      # but with a layer per package, so a new version only pulls what changed. `nix build
      # .#image` makes a script that writes the image: `./result | docker load`.
      image = pkgs.dockerTools.streamLayeredImage {
        name = "daily-dashboard";
        tag = "latest";
        fakeRootCommands = ''
          mkdir -p app/site/data app/docs tmp
          ln -s ${container}/bin/health-dashboard-container entrypoint
          ln -s ${timewHook} app/site/.timew-hook
          cp -r ${./web} app/site/web
          cp ${./plan.example.json} app/site/plan.example.json
          cp ${../docs/README.md} app/docs/README.md
          cp ${../docs/dashboard.md} app/docs/dashboard.md
          cp ${../docs/noctalia-plugin.md} app/docs/noctalia-plugin.md
          cp -r ${../docs/templates} app/docs/templates
          chmod -R u+w app
          chown -R 1000:1000 app/site/data
          chmod 1777 tmp
        '';
        config = {
          Entrypoint = [ "/entrypoint" ];
          Cmd = [ "1628" ];
          User = "1000:1000";
          Env = [ "DASHBOARD_SITE=/app/site" "DASHBOARD_LISTEN=0.0.0.0:1628" "HOME=/app/site/data/home" ];
          Volumes."/app/site/data" = { };
          ExposedPorts."1628/tcp" = { };
          Labels."org.opencontainers.image.source" = "https://github.com/shibahex/daily-dashboard";
        };
      };
    in
    {
      packages.${system} = {
        default = dashboard;
        server = server;
        timew-hook = timewHook;
        container = container;
        image = image;
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
