#!/usr/bin/env python3
"""Throwaway: compare two glocker config files stanza by stanza.

Line-based (so it copes with the huge domains list and !include lines): for each
top-level stanza it lists the direct child keys, and flags keys present in one
file but not the other. Contents of `domains:` are ignored.

    ./compare_conf.py [real] [sample]     # defaults: conf/conf.yaml conf.yaml.sample

rm compare_conf.py when done.
"""
import re
import sys


def stanzas(path):
    """Ordered {top_key: {'kind': 'scalar'|'map'|'seq', 'keys': [child keys]}}."""
    out = {}
    cur = None
    for raw in open(path):
        line = raw.rstrip("\n")
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        indent = len(line) - len(line.lstrip(" "))
        s = line.strip()
        if indent == 0:
            m = re.match(r"^([A-Za-z_]\w*):(.*)$", s)
            if m:
                cur = m.group(1)
                out[cur] = {"kind": "scalar" if m.group(2).strip() else "?", "keys": []}
            else:
                cur = None
        elif cur is not None and indent == 2 and out[cur]["kind"] != "seq":
            if s.startswith("- "):
                out[cur]["kind"] = "seq"
            else:
                m = re.match(r"^([A-Za-z_]\w*):", s)
                if m:
                    out[cur]["kind"] = "map"
                    out[cur]["keys"].append(m.group(1))
    return out


def main():
    real = sys.argv[1] if len(sys.argv) > 1 else "conf/conf.yaml"
    sample = sys.argv[2] if len(sys.argv) > 2 else "conf/conf.yaml.sample"
    a, b = stanzas(real), stanzas(sample)

    order = list(dict.fromkeys(list(a) + list(b)))
    print(f"\n{'STANZA':<24}{'real':<6}{'sample':<8}KEY DIFFERENCE")
    print("-" * 78)
    for k in order:
        if k == "domains":
            continue
        ra, rb = a.get(k), b.get(k)
        in_a = "yes" if ra else "-"
        in_b = "yes" if rb else "-"
        ka = set(ra["keys"]) if ra else set()
        kb = set(rb["keys"]) if rb else set()
        note = ""
        if not ra:
            note = "(sample only)"
        elif not rb:
            note = "(real only)"
        else:
            only_a, only_b = sorted(ka - kb), sorted(kb - ka)
            if only_a:
                note += f"only in real: {only_a}  "
            if only_b:
                note += f"only in sample: {only_b}"
            if not note:
                kind = ra["kind"]
                note = "(match)" if kind == "map" else f"({kind})"
        print(f"{k:<24}{in_a:<6}{in_b:<8}{note}")
    print("-" * 78)
    print("domains: skipped (compared structurally elsewhere)\n")


if __name__ == "__main__":
    main()
