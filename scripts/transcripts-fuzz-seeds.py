#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
"""Write fuzz seeds for the codecs' FuzzCodec from the bodies of the
recorded transcripts and the specification cases (testdata/transcripts).

    python3 -I scripts/transcripts-fuzz-seeds.py

One seed per distinct body that another implementation sent (and per
specification case), at most PER_KIND of each kind, named by the SHA256
of the body so that a rerun writes the same files.
"""
import glob, hashlib, json, os, re

PER_KIND = 6
KIND = {"program": 0, "event": 1, "report": 2, "ven": 3, "resource": 4, "subscription": 5,
        "resource-group": 6, "notification": 7, "notifiers": 8, "topics": 9, "problem": 10}
COLL = {"programs": "program", "events": "event", "reports": "report", "vens": "ven", "resources": "resource",
        "subscriptions": "subscription", "resource_groups": "resource-group"}
OUT = {"3.1.0": "internal/wire/v310/testdata/fuzz/FuzzCodec", "3.0.1": "internal/wire/v301/testdata/fuzz/FuzzCodec"}


def kind_of(route, method, request):
    parts = route.strip("/").split("/")
    if route == "/notifiers":
        return "notifiers"
    if route.startswith("/notifiers/mqtt/topics/"):
        return "topics"
    k = COLL.get(parts[0])
    if parts[0] == "vens" and len(parts) >= 3 and parts[2] == "resources":
        k = "resource"
    return k


def seed(kind, body):
    return "go test fuzz v1\nuint8(%d)\n[]byte(%s)\n" % (KIND[kind], json.dumps(body.decode("utf-8"), ensure_ascii=True))


def main():
    seeds = {v: {} for v in OUT}
    for path in sorted(glob.glob("testdata/transcripts/*/*.json")):
        if "/spec/" in path or "/fixtures/" in path:
            continue
        f = json.load(open(path))
        v = f["protocolVersion"]
        for s in f["steps"]:
            bodies = []
            if s.get("message"):
                bodies.append(("notification", s["message"].get("body")))
            if s.get("request"):
                q, r = s["request"], s.get("response") or {}
                if s["transport"] == "webhook":
                    bodies.append(("notification", q.get("body")))
                elif s["origin"] == "peer" and q["method"] in ("POST", "PUT"):
                    bodies.append((kind_of(q["route"], q["method"], True), q.get("body")))
                elif s["origin"] == "self" and r.get("bodyFormat") == "json":
                    k = "problem" if r["status"] >= 400 else kind_of(q["route"], q["method"], False)
                    b = r.get("body")
                    if isinstance(b, list):
                        b = b[0] if b else None
                    bodies.append((k, b))
            for k, b in bodies:
                if k not in KIND or b is None:
                    continue
                raw = json.dumps(b, separators=(",", ":")).encode()
                seeds[v].setdefault(k, {})[hashlib.sha256(raw).hexdigest()] = raw
    for v in OUT:
        cases = json.load(open("testdata/transcripts/spec/%s/cases.json" % v))["cases"]
        names = {"program": "program", "event": "event", "report": "report", "ven": "ven", "resource": "resource",
                 "subscription": "subscription", "notification": "notification", "notifiersResponse": "notifiers",
                 "notifierTopicsResponse": "topics", "problem": "problem"}
        for c in cases:
            k = names.get(c["schema"])
            if k:
                raw = json.dumps(c["instance"], separators=(",", ":")).encode()
                seeds[v].setdefault(k, {})[hashlib.sha256(raw).hexdigest()] = raw
    for v, out in OUT.items():
        os.makedirs(out, exist_ok=True)
        for old in glob.glob(out + "/transcript-*"):
            os.remove(old)
        n = 0
        for k in sorted(seeds[v]):
            for h in sorted(seeds[v][k])[:PER_KIND]:
                with open("%s/transcript-%s-%s" % (out, k, h[:16]), "w") as fh:
                    fh.write(seed(k, seeds[v][k][h]))
                n += 1
        print(v, n, "seeds in", out)


if __name__ == "__main__":
    main()
