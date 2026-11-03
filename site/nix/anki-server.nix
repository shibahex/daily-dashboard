# Anki for running on the server without a screen, one per user, inside the dashboard's sandbox
# (internal/ankiserver): Anki with AnkiConnect and the dashboard's own add-on (anki-server/dashboard:
# AnkiWeb sign-in, syncing without dialogs, studying), and a launcher.
#
# `dashboard-anki <base folder>` sets up the folder's profile (anki-server/seed.py, so no first-run
# dialog appears) and starts Anki with AnkiConnect on 127.0.0.1:8765.
{ anki, ankiAddons, anki-utils, python3, writeShellApplication }:

let
  dashboardAddon = anki-utils.buildAnkiAddon {
    pname = "dashboard";
    version = "1";
    src = ./anki-server/dashboard;
  };
  ankiWithAddons = anki.withAddons [ ankiAddons.anki-connect dashboardAddon ];
in
writeShellApplication {
  name = "dashboard-anki";
  text = ''
    base="$1"
    mkdir -p "$base"
    # Anki reads the user name while it loads; inside the sandbox there's none.
    export USER="''${USER:-anki}" ANKI_SINGLE_INSTANCE_KEY=dashboard
    PYTHONPATH=${anki.lib}/${python3.sitePackages} ${python3}/bin/python -P -s ${./anki-server/seed.py} "$base"

    # No screen; Chromium's own sandbox can't run inside the dashboard's, which is the boundary.
    export QT_QPA_PLATFORM=offscreen QTWEBENGINE_DISABLE_SANDBOX=1 QTWEBENGINE_CHROMIUM_FLAGS=--disable-gpu
    exec ${ankiWithAddons}/bin/anki -b "$base" -p "User 1"
  '';
}
