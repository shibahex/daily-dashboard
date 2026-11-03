"""Sets up an Anki base folder for running without a screen: one profile, English, and nothing
that would open a dialog (the first-run language choice, update checks, syncing on open or
close). Safe to run on every start. DASHBOARD_ANKI_SYNC_URL syncs with a self-hosted sync
server instead of AnkiWeb."""

import os
import sys
from pathlib import Path

import anki.lang
from aqt.profiles import ProfileManager

PROFILE = "User 1"


def main(base: str) -> None:
    anki.lang.set_lang("en_US")  # creating a profile needs translations
    pm = ProfileManager(Path(base))
    pm.setupMeta()
    pm.meta["defaultLang"] = pm.meta.get("defaultLang") or "en_US"
    pm.set_update_check(False)
    pm.set_check_for_addon_updates(False)
    if PROFILE not in pm.profiles():
        pm.create(PROFILE)
    pm.load(PROFILE)
    pm.profile["autoSync"] = False  # the dashboard syncs, without dialogs
    pm.profile["syncMedia"] = True
    # A self-hosted sync server instead of AnkiWeb, when the admin sets one.
    pm.set_custom_sync_url(os.environ.get("DASHBOARD_ANKI_SYNC_URL") or None)
    pm.set_last_loaded_profile_name(PROFILE)
    pm.save()


if __name__ == "__main__":
    main(sys.argv[1])
