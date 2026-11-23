import base64
import json
import os
import re
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

TRAFFIC_STATE = "/etc/sing-box/user_manager/traffic/state.json"
LIMIT_DIR = "/etc/sing-box/user_manager/limits"
URL_DIR = "/etc/sing-box/url"

def format_bytes(value):
    try:
        value = float(value)
    except Exception:
        value = 0
    if value >= 1024**3:
        return f"{value/1024**3:.2f} GB"
    if value >= 1024**2:
        return f"{value/1024**2:.2f} MB"
    if value >= 1024:
        return f"{value/1024:.2f} KB"
    return f"{int(value)} B"

import time
def load_json(path, retries=3, delay=0.05):
    for _ in range(retries):
        try:
            with open(path, "r", encoding="utf-8") as f:
                return json.load(f)
        except Exception:
            time.sleep(delay)
    return {}

def make_subscription(username):
    if not re.fullmatch(r"[A-Za-z0-9._-]+", username):
        return None
    links_file = os.path.join(URL_DIR, username, username)
    if not os.path.isfile(links_file):
        return None
    try:
        with open(links_file, "r", encoding="utf-8") as f:
            links = f.read().strip()
    except Exception:
        return None

    state = load_json(TRAFFIC_STATE)
    user_data = state.get("users", {}).get(username, {})
    used = int(user_data.get("period_total", 0) or 0)
    
    limit_file = os.path.join(LIMIT_DIR, f"{username}.json")
    limit_data = load_json(limit_file)
    enabled = bool(limit_data.get("enabled", False))
    limit_bytes = int(limit_data.get("limit_bytes", 0) or 0)
    
    if enabled and limit_bytes > 0:
        remaining = max(limit_bytes - used, 0)
        remark = f"📊 剩余流量: {format_bytes(remaining)} | 已用: {format_bytes(used)}"
    else:
        remark = f"📊 剩余流量: 无限制 | 已用: {format_bytes(used)}"
    
    # 加入 URL 编码，防止特殊字符导致客户端排序混乱或不显示
    safe_remark = urllib.parse.quote(remark)
    traffic_line = f"vless://00000000-0000-0000-0000-000000000000@127.0.0.1:10000?encryption=none&security=none&type=tcp#{safe_remark}"
    
    # 物理上强制拼接在第一行
    content = traffic_line + "\n" + links
    return base64.b64encode(content.encode("utf-8")).decode("ascii") + "\n"

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        match = re.fullmatch(r"/sub/([A-Za-z0-9._-]+)", self.path)
        if not match:
            self.send_response(404)
            self.end_headers()
            return
        content = make_subscription(match.group(1))
        if content is None:
            self.send_response(404)
            self.end_headers()
            return
        data = content.encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "text/plain; charset=utf-8")
        self.send_header("Content-Length", str(len(data)))
        self.send_header("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
        self.send_header("Pragma", "no-cache")
        self.end_headers()
        self.wfile.write(data)
    def log_message(self, format, *args):
        return

ThreadingHTTPServer(("127.0.0.1", 18080), Handler).serve_forever()
