"""The dashboard's actions for an Anki that runs on its server, without a screen.

They're added to AnkiConnect (so the dashboard reaches them the same way as the rest): signing in
to AnkiWeb, syncing without the dialogs Anki would show, and studying (the next card, answering
it), which AnkiConnect only offers through Anki's own review screen.
"""

import importlib
import re
import time

import aqt
from anki.cards import Card
from anki.scheduler.v3 import CardAnswer
from anki.sound import SoundOrVideoTag

RATINGS = {1: CardAnswer.AGAIN, 2: CardAnswer.HARD, 3: CardAnswer.GOOD, 4: CardAnswer.EASY}
PLAY = re.compile(r"\[anki:play:([qa]):(\d+)\]")

# When each card was shown, for the time it took (by card id). Only the last few are kept.
_shown: dict[int, float] = {}


def _api(func):
    func.api = True
    func.versions = ()
    return func


def _mw():
    if aqt.mw is None or aqt.mw.col is None:
        raise Exception("Anki's collection isn't open")
    return aqt.mw


@_api
def dashboardStatus(self):
    mw = _mw()
    return {
        "signedIn": bool(mw.pm.sync_auth()),
        "username": mw.pm.profile.get("syncUser") or "",
    }


@_api
def dashboardLogin(self, username, password):
    mw = _mw()
    auth = mw.col.sync_login(username=username, password=password, endpoint=mw.pm.sync_endpoint())
    mw.pm.set_sync_key(auth.hkey)
    mw.pm.set_sync_username(username)
    mw.pm.save()
    return dashboardSync(self)


@_api
def dashboardLogout(self):
    mw = _mw()
    mw.pm.clear_sync_auth()
    mw.pm.save()
    return True


@_api
def dashboardSync(self):
    """Syncs with AnkiWeb. When AnkiWeb and this copy can't be merged, AnkiWeb's copy wins: a full
    download, unless AnkiWeb has no cards yet (then this copy is uploaded)."""
    mw = _mw()
    auth = mw.pm.sync_auth()
    if not auth:
        raise Exception("Not signed in to AnkiWeb")
    media = mw.pm.media_syncing_enabled()
    out = mw.col.sync_collection(auth, media)
    mw.pm.set_host_number(out.host_number)
    if out.new_endpoint:
        mw.pm.set_current_sync_url(out.new_endpoint)
        auth = mw.pm.sync_auth()
    # NO_CHANGES: nothing more to do (Anki has already exchanged any changes).
    result = {out.NO_CHANGES: "synced", out.NORMAL_SYNC: "synced"}.get(out.required)
    if result is None:
        upload = out.required == out.FULL_UPLOAD
        server_usn = out.server_media_usn if media else None
        mw.col.close_for_full_sync()
        try:
            mw.col.full_upload_or_download(auth=auth, server_usn=server_usn, upload=upload)
        finally:
            mw.reopen(after_full_sync=True)
            mw.reset()
        result = "uploaded" if upload else "downloaded"
    if media:
        mw.col.sync_media(auth)
    mw.pm.save()
    return {"result": result, "message": out.server_message}


def _render(text, tags, side):
    """The card's HTML with each [sound:] as an <audio> the dashboard fills in (TTS is dropped)."""

    def sound(match):
        if match.group(1) != side:
            return ""
        index = int(match.group(2))
        if index < len(tags) and isinstance(tags[index], SoundOrVideoTag):
            return '<audio controls data-sound="%s"></audio>' % _escape(tags[index].filename)
        return ""

    return PLAY.sub(sound, text)


def _escape(s):
    return s.replace("&", "&amp;").replace('"', "&quot;").replace("<", "&lt;")


@_api
def dashboardStudyNext(self, deck=None):
    """The next card to study in deck (its name; None = the current deck), or done when nothing
    is left, with the counts left and the answer buttons' next intervals."""
    mw = _mw()
    col = mw.col
    if deck:
        did = col.decks.id_for_name(deck)
        if not did:
            raise Exception("No such deck")
        col.decks.select(did)
    queued = col.sched.get_queued_cards(fetch_limit=1)
    counts = [queued.new_count, queued.learning_count, queued.review_count]
    current = col.decks.current()
    if not queued.cards:
        return {"done": True, "counts": counts, "deck": current["name"]}
    top = queued.cards[0]
    card = Card(col, backend_card=top.card)
    out = card.render_output(reload=True)
    if len(_shown) > 50:
        _shown.clear()
    _shown.setdefault(card.id, time.time())
    return {
        "done": False,
        "counts": counts,
        "deck": current["name"],
        "card": {
            "id": card.id,
            "reps": card.reps,  # sent back with the answer, so it can't count twice
            "ord": card.ord,
            "question": _render(out.question_text, out.question_av_tags, "q"),
            "answer": _render(out.answer_text, out.answer_av_tags, "a"),
            "css": out.css,
            "buttons": list(col.sched.describe_next_states(top.states)),
        },
    }


@_api
def dashboardStudyAnswer(self, cardId, reps, ease):
    """Answers the card being studied (ease 1-4: Again, Hard, Good, Easy). It must still be the
    next card, unanswered since it was shown (reps), so an answer sent twice, or for a card
    already answered elsewhere, is refused."""
    mw = _mw()
    col = mw.col
    rating = RATINGS.get(int(ease))
    if rating is None:
        raise Exception("ease must be 1-4")
    queued = col.sched.get_queued_cards(fetch_limit=1)
    if not queued.cards or queued.cards[0].card.id != int(cardId) or queued.cards[0].card.reps != int(reps):
        raise Exception("That card isn't the next one any more")
    top = queued.cards[0]
    card = Card(col, backend_card=top.card)
    card.timer_started = _shown.pop(card.id, time.time())
    col.sched.answer_card(col.sched.build_answer(card=card, states=top.states, rating=rating))
    return True


@_api
def dashboardStudyUndo(self):
    """Undoes the last change (an answer, when studying)."""
    mw = _mw()
    if not mw.col.undo_status().undo:
        raise Exception("Nothing to undo")
    mw.col.undo()
    return True


@_api
def dashboardQuit(self):
    """Closes Anki (saving the collection) once this answer has been sent. AnkiConnect's
    guiExitAnki doesn't: its timer is dropped before it fires."""
    from aqt.qt import QTimer

    mw = _mw()
    QTimer.singleShot(300, mw.unloadProfileAndExit)
    return True


def _attach():
    """Adds the actions to AnkiConnect, which finds actions on its class."""
    try:
        ankiconnect = importlib.import_module("anki-connect")
    except ImportError:
        return
    for func in (dashboardStatus, dashboardLogin, dashboardLogout, dashboardSync,
                 dashboardStudyNext, dashboardStudyAnswer, dashboardStudyUndo, dashboardQuit):
        setattr(ankiconnect.AnkiConnect, func.__name__, func)


_attach()
