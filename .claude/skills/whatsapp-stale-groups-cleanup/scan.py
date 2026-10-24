"""Find stale WhatsApp groups and log cleanup decisions.

Reads messages.db / whatsapp.db read-only (same defaults as the MCP server).

  python scan.py scan [--days 180] [--recent 14]
      Writes cleanup/<today>-candidates.json (oldest first) and prints a summary.
  python scan.py log <index> <action> [result]
      Appends candidate <index> (1-based) to cleanup/<today>.csv.
"""

import argparse
import csv
import glob
import json
import os
import re
import sqlite3
import sys
from collections import Counter
from datetime import UTC, date, datetime

REPO = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", ".."))
STORE = os.path.join(REPO, "whatsapp-bridge", "store")
MESSAGES_DB = os.environ.get("WHATSAPP_DB_PATH", os.path.join(STORE, "messages.db"))
WHATSMEOW_DB = os.environ.get("WHATSMEOW_DB_PATH", os.path.join(STORE, "whatsapp.db"))
WORK = os.path.join(REPO, "cleanup")
TODAY = date.today().isoformat()
CANDIDATES = os.path.join(WORK, f"{TODAY}-candidates.json")
LOG = os.path.join(WORK, f"{TODAY}.csv")
LOG_HEADER = ["name", "jid", "last_activity", "reasons", "action", "result"]

# One-off event names: keywords, past years, dates like 05.07.25 / 22.02 / 13th.
EVENT = re.compile(
    r"party|dinner|ap[eé]ro|bday|b'day|b-day|birthday|compleanno|gift|present|wedding|matrimonio|"
    r"bachelor|stag|trip|holiday|weekend|housewarming|halloween|xmas|secret santa|farewell|"
    r"festa|tournament|drinks?\b|tonight|quick question|plan for|"
    r"\b(19|20)\d\d\b|['‘’]\d\d\b|\b\d{1,2}[./]\d{1,2}([./]\d{2,4})?\b|\b\d{1,2}(st|nd|rd|th)\b",
    re.I,
)


def connect(path):
    return sqlite3.connect(f"file:{path}?mode=ro", uri=True)


def parse_ts(value):
    if value is None:
        return None
    s = str(value).replace("T", " ")
    for fmt in ("%Y-%m-%d %H:%M:%S%z", "%Y-%m-%d %H:%M:%S.%f%z"):
        try:
            return datetime.strptime(s, fmt)
        except ValueError:
            pass
    return None


def contact_names():
    names = {}
    try:
        w = connect(WHATSMEOW_DB)
        for jid, full, push in w.execute("SELECT their_jid, full_name, push_name FROM whatsmeow_contacts"):
            names[jid.split("@")[0]] = full or push
        for lid, pn in w.execute("SELECT lid, pn FROM whatsmeow_lid_map"):
            if pn in names:
                names.setdefault(lid, names[pn])
    except sqlite3.Error:
        pass
    return names


def decided_before():
    """JIDs with a decision in any run's log, today's included (skipped or already handled)."""
    seen = {}
    for path in sorted(glob.glob(os.path.join(WORK, "*.csv"))):
        with open(path, encoding="utf-8") as f:
            for row in csv.DictReader(f):
                seen[row["jid"]] = (row["action"], os.path.basename(path)[:10])
    return seen


def scan(days, recent):
    c = connect(MESSAGES_DB)
    names = contact_names()
    earlier = decided_before()
    now = datetime.now(UTC)
    out, total, excluded = [], 0, 0
    for name, jid in c.execute("SELECT COALESCE(name, ''), jid FROM chats WHERE jid LIKE '%@g.us'").fetchall():
        total += 1
        if jid in earlier:
            excluded += 1
            continue
        last = c.execute(
            "SELECT timestamp, sender, content, is_from_me, media_type FROM messages"
            " WHERE chat_jid = ? ORDER BY timestamp DESC LIMIT 1",
            (jid,),
        ).fetchone()
        mine = c.execute(
            "SELECT MAX(timestamp) FROM messages WHERE chat_jid = ? AND is_from_me = 1", (jid,)
        ).fetchone()[0]
        # chats.last_message_time moves without new messages (history sync,
        # read markers), so only a stored message dates real activity.
        activity = parse_ts(last[0]) if last else None
        age = (now - activity).days if activity else None
        is_event = bool(EVENT.search(name))

        reasons = []
        if last is None:
            reasons.append("no messages")
        elif age > days:
            reasons.append("inactive")
        if is_event:
            reasons.append("past event?")
        if not reasons or (age is not None and age <= recent):
            continue
        if last is None and not is_event:
            continue  # nothing to go on: may be busy on the phone

        sender = text = ""
        if last:
            raw = (last[1] or "").split("@")[0]
            if last[3]:
                sender = "me"
            elif raw and raw != jid.split("@")[0]:
                sender = names.get(raw) or raw
            text = (last[2] or (f"[{last[4]}]" if last[4] else "")).replace("\n", " ")[:60]
        out.append(
            {
                "name": name,
                "jid": jid,
                "last_activity": activity.isoformat() if activity else "",
                "age_days": age,
                "last_sender": sender,
                "last_text": text,
                "my_last_message": str(mine)[:10] if mine else "",
                "reasons": reasons,
            }
        )

    # Dated groups oldest first; groups with no stored messages last.
    out.sort(key=lambda x: (x["last_activity"] == "", x["last_activity"]))
    for i, x in enumerate(out, 1):
        x["index"] = i
    os.makedirs(WORK, exist_ok=True)
    with open(CANDIDATES, "w", encoding="utf-8") as f:
        json.dump(out, f, ensure_ascii=False, indent=1)
    if not os.path.exists(LOG):
        with open(LOG, "w", newline="", encoding="utf-8") as f:
            csv.writer(f).writerow(LOG_HEADER)

    counts = Counter(r for x in out for r in x["reasons"])
    print(f"groups={total} already_decided={excluded} candidates={len(out)} reasons={dict(counts)}")
    print(f"candidates file: {CANDIDATES}")
    for x in out:
        when = x["last_activity"][:10] or "no stored messages"
        print(
            f"{x['index']:>3} | {x['name']} | {x['jid']} | {when} | {','.join(x['reasons'])} | {x['last_sender']}: {x['last_text']}"
        )


def log(index, action, result):
    with open(CANDIDATES, encoding="utf-8") as f:
        x = json.load(f)[index - 1]
    with open(LOG, "a", newline="", encoding="utf-8") as f:
        csv.writer(f).writerow([x["name"], x["jid"], x["last_activity"], ";".join(x["reasons"]), action, result])


def main():
    sys.stdout.reconfigure(encoding="utf-8")
    p = argparse.ArgumentParser()
    sub = p.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("scan")
    s.add_argument("--days", type=int, default=180)
    s.add_argument("--recent", type=int, default=14)
    lg = sub.add_parser("log")
    lg.add_argument("index", type=int)
    lg.add_argument("action", choices=["skip", "leave", "leave+delete"])
    lg.add_argument("result", nargs="?", default="")
    a = p.parse_args()
    if a.cmd == "scan":
        scan(a.days, a.recent)
    else:
        log(a.index, a.action, a.result)


if __name__ == "__main__":
    main()
