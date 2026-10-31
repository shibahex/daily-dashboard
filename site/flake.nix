{
  description = "Health dashboard: local server + Taskwarrior/Timewarrior integration";

  inputs.nixpkgs.url = "nixpkgs";
  # Builds the published image and pushes only the layers the registry doesn't have yet.
  inputs.nix2container.url = "github:nlewo/nix2container";
  inputs.nix2container.inputs.nixpkgs.follows = "nixpkgs";

  outputs = { self, nixpkgs, nix2container }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
      n2c = nix2container.packages.${system}.nix2container;
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

      # The Go server (cmd/dashboard). Its one dependency is goldmark, for Markdown. Built from the
      # Go files only, so a change to the pages doesn't build it again.
      server = pkgs.buildGoModule {
        pname = "health-dashboard-server";
        version = "0.1.0";
        src = pkgs.lib.fileset.toSource {
          root = ./.;
          fileset = pkgs.lib.fileset.unions [ ./go.mod ./go.sum ./cmd ./internal ];
        };
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

      # What the image has outside /nix/store: the entrypoint, the pages, the guides and the data folder.
      imageRoot = pkgs.runCommand "daily-dashboard-root" { } ''
        mkdir -p $out/app/site/data $out/app/docs $out/tmp
        ln -s ${container}/bin/health-dashboard-container $out/entrypoint
        ln -s ${timewHook} $out/app/site/.timew-hook
        cp -r ${./web} $out/app/site/web
        cp ${./plan.example.json} $out/app/site/plan.example.json
        cp ${../docs/README.md} $out/app/docs/README.md
        cp ${../docs/dashboard.md} $out/app/docs/dashboard.md
        cp ${../docs/noctalia-plugin.md} $out/app/docs/noctalia-plugin.md
        cp -r ${../docs/templates} $out/app/docs/templates
      '';

      # The image GitHub publishes (.github/workflows/docker.yml), the same as ../Dockerfile makes
      # but with a layer per package, so a new version only pulls what changed. nix2container
      # pushes it straight from the Nix store, skipping the layers the registry already has:
      # `nix run .#image.copyTo -- docker://<name>:<tag>`, or `.#image.copyToDockerDaemon`.
      image = n2c.buildImage {
        name = "daily-dashboard";
        tag = "latest";
        # Every package the server needs gets its own layer (95 of them), and the pages one more,
        # so an update only pushes and pulls the layers that changed.
        layers = [ (n2c.buildLayer { deps = [ container ]; maxLayers = 110; }) ];
        copyToRoot = [ imageRoot ];
        perms = [
          { path = imageRoot; regex = ".*"; orMode = "0200"; }
          { path = imageRoot; regex = "/app/site/data$"; mode = "0755"; uid = 1000; gid = 1000; }
          { path = imageRoot; regex = "/tmp$"; mode = "1777"; }
        ];
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
