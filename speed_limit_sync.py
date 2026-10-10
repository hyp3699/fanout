#!/usr/bin/env python3
# 由 sb.sh 生成，勿手动修改。
# 根据 speed_limits.json 生成 /etc/sing-box/conf/00-limiter.json：
# 限速用户先进入 bandwidth-limiter，再在其内部 route 中执行与外层相同的分流规则，
# 因此同一用户名的限速和分流同时生效。
import copy, glob, json, os, shutil, subprocess, sys

CONF_DIR = os.environ.get("SB_CONF_DIR", "/etc/sing-box/conf")
STORE = os.environ.get("SB_SPEED_STORE", "/etc/sing-box/user_manager/speed_limits.json")
SB_BIN = os.environ.get("SB_BIN", "/etc/sing-box/sing-box")
OUT = os.path.join(CONF_DIR, "00-limiter.json")
TAG = "limit-users"


def load(fn):
    try:
        with open(fn, encoding="utf-8") as f:
            return json.load(f)
    except Exception:
        return None


def build():
    store = load(STORE) or {}
    users = store.get("users", {}) if isinstance(store, dict) else {}
    users = {k: v for k, v in users.items() if isinstance(v, dict) and v.get("speed")}
    if not users:
        return None
    rules, final = [], ""
    for fn in sorted(glob.glob(os.path.join(CONF_DIR, "*.json"))):
        if os.path.abspath(fn) == os.path.abspath(OUT):
            continue
        data = load(fn)
        if not isinstance(data, dict) or not isinstance(data.get("route"), dict):
            continue
        r = data["route"]
        for rule in r.get("rules", []) or []:
            if not isinstance(rule, dict) or rule.get("outbound") == TAG:
                continue
            if rule.get("action") == "sniff":
                continue
            rules.append(copy.deepcopy(rule))
        if r.get("final"):
            final = r["final"]
    names = sorted(users)
    return {
        "outbounds": [{
            "type": "bandwidth-limiter",
            "tag": TAG,
            "strategy": "users",
            "users": [{
                "name": n,
                "strategy": "global",
                "mode": users[n].get("mode", "bidirectional"),
                "speed": users[n]["speed"],
            } for n in names],
            "route": {"rules": rules, "final": final or "direct"},
        }],
        "route": {"rules": [
            {"auth_user": names, "action": "sniff"},
            {"auth_user": names, "outbound": TAG},
        ]},
    }


def main():
    cfg = build()
    bak = OUT + ".bak"
    had_old = os.path.exists(OUT)
    if had_old:
        shutil.copyfile(OUT, bak)
    if cfg is None:
        if had_old:
            os.remove(OUT)
    else:
        with open(OUT + ".tmp", "w", encoding="utf-8") as f:
            json.dump(cfg, f, ensure_ascii=False, indent=2)
        os.replace(OUT + ".tmp", OUT)
    if cfg is not None and os.path.exists(SB_BIN) and "--no-check" not in sys.argv:
        p = subprocess.run([SB_BIN, "check", "-C", CONF_DIR], capture_output=True, text=True)
        if p.returncode != 0:
            if had_old:
                os.replace(bak, OUT)
            else:
                os.remove(OUT)
            sys.stderr.write((p.stderr or p.stdout).strip() + "\n")
            sys.exit(2)
    if os.path.exists(bak):
        os.remove(bak)


if __name__ == "__main__":
    main()
