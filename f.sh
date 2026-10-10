#!/usr/bin/env bash
# fanout 管理菜单
set -uo pipefail

WORK_DIR=/var/lib/fanout
SERVICE=fanout
BIN=/usr/local/bin/fanout
REPO="${REPO:-hyp3699/fanout}"

G='\033[0;32m'; R='\033[0;31m'; Y='\033[0;33m'; B='\033[0;36m'; D='\033[2m'; N='\033[0m'

need_root() {
  [[ $EUID -eq 0 ]] || { echo -e "${R}需要 root${N}"; exit 1; }
}

# ── init 系统抽象：systemd 与 OpenRC ────────────────────
if command -v systemctl >/dev/null 2>&1 && [[ -d /run/systemd/system ]]; then
  INIT_SYS=systemd
  UNIT=/etc/systemd/system/${SERVICE}.service
else
  INIT_SYS=openrc
  UNIT=/etc/init.d/${SERVICE}
fi

svc_start()   { [[ $INIT_SYS == systemd ]] && systemctl start "$SERVICE"   || rc-service "$SERVICE" start; }
svc_stop()    { [[ $INIT_SYS == systemd ]] && systemctl stop "$SERVICE"    || rc-service "$SERVICE" stop; }
svc_restart() { [[ $INIT_SYS == systemd ]] && systemctl restart "$SERVICE" || rc-service "$SERVICE" restart; }
svc_reload()  { [[ $INIT_SYS == systemd ]] && systemctl daemon-reload || true; }
svc_enable()  { [[ $INIT_SYS == systemd ]] && systemctl enable "$SERVICE" >/dev/null 2>&1 || rc-update add "$SERVICE" default >/dev/null 2>&1; }
svc_disable() { [[ $INIT_SYS == systemd ]] && systemctl disable "$SERVICE" >/dev/null 2>&1 || rc-update del "$SERVICE" default >/dev/null 2>&1; }

svc_is_enabled() {
  if [[ $INIT_SYS == systemd ]]; then
    systemctl is-enabled --quiet "$SERVICE"
  else
    rc-update show default 2>/dev/null | grep -q "^ *${SERVICE} "
  fi
}

svc_enabled_text() {
  svc_is_enabled && echo enabled || echo disabled
}

svc_status_page() {
  if [[ $INIT_SYS == systemd ]]; then
    systemctl status "$SERVICE" --no-pager
  else
    rc-service "$SERVICE" status
  fi
}

svc_logs() {
  if [[ $INIT_SYS == systemd ]]; then
    journalctl -u "$SERVICE" -n "${1:-50}" --no-pager
  else
    tail -n "${1:-50}" /var/log/${SERVICE}.log 2>/dev/null || echo "  暂无日志"
  fi
}

svc_logs_follow() {
  if [[ $INIT_SYS == systemd ]]; then
    journalctl -u "$SERVICE" -f
  else
    tail -f /var/log/${SERVICE}.log
  fi
}

svc_state() {
  if [[ $INIT_SYS == systemd ]]; then
    systemctl is-active --quiet "$SERVICE" && echo running || echo stopped
  else
    rc-service "$SERVICE" status >/dev/null 2>&1 && echo running || echo stopped
  fi
}

# 端口以 settings.json 为准。老版本把 -web 写死在服务文件里，
# 两处各改各的会互相拽回旧值，所以这里只认工作目录下的配置。
web_port() {
  local p
  p=$(sed -n 's/.*"port"[[:space:]]*:[[:space:]]*\([0-9]*\).*/\1/p' \
        "$WORK_DIR/settings.json" 2>/dev/null | head -1)
  [[ -n $p ]] && { echo "$p"; return; }
  # 兼容老安装：settings.json 还没生成时退回读服务文件
  grep -oE '\-web [0-9]+' "$UNIT" 2>/dev/null \
    | grep -oE '[0-9]+' | head -1 || echo 8899
}

public_ip() {
  curl -s --max-time 6 http://api.ipify.org 2>/dev/null || echo "<本机IP>"
}

pause() {
  echo
  read -rp "回车返回菜单..." _
}

show_info() {
  local state port bp pw ip
  state=$(svc_state); port=$(web_port)
  bp=$(cat "$WORK_DIR/basepath" 2>/dev/null || echo "-")
  pw=$(cat "$WORK_DIR/password" 2>/dev/null || echo "-")
  ip=$(public_ip)

  echo
  if [[ $state == running ]]; then
    echo -e "  状态      ${G}运行中${N}"
  else
    echo -e "  状态      ${R}已停止${N}"
  fi
  echo -e "  版本      $("$BIN" -version 2>/dev/null || echo '-')"
  echo -e "  开机自启  $(svc_enabled_text)"
  echo
  echo -e "  ${B}管理地址  http://${ip}:${port}/${bp}/${N}"
  echo -e "  ${B}访问口令  ${pw}${N}"
  echo

  local n
  n=$(ls -d /var/run/netns/fo* 2>/dev/null | wc -l | tr -d ' ')
  echo -e "  ${D}运行中的隧道: ${n}${N}"
}

list_tunnels() {
  local port bp pw ck
  port=$(web_port)
  bp=$(cat "$WORK_DIR/basepath" 2>/dev/null)
  pw=$(cat "$WORK_DIR/password" 2>/dev/null)
  ck=$(mktemp)

  curl -s --max-time 10 -c "$ck" -X POST -d "password=${pw}" \
    "http://127.0.0.1:${port}/${bp}/login" -o /dev/null
  echo
  curl -s --max-time 10 -b "$ck" "http://127.0.0.1:${port}/${bp}/api/tunnels" \
    > "$ck.json" 2>/dev/null
  rm -f "$ck"

  # 用 sed/awk 解析而不是 python3/jq：Alpine 最小安装两者都没有，
  # 为了一条列表命令再拉依赖不值当。字段固定，按对象拆行足够稳。
  if [[ ! -s "$ck.json" ]] || ! grep -q '"port"' "$ck.json" 2>/dev/null; then
    echo "  还没有隧道，去网页里添加"
  else
    printf "  %-10s%-11s%-18s%s\n" "端口" "状态" "出口 IP" "节点"
    # 按 {"slot" 切分而不是按 }：node 是嵌套对象，按 } 切会把一条记录劈成两半
    sed 's/{"slot"/\n{"slot"/g' "$ck.json" | while IFS= read -r line; do
      case "$line" in *'"slot"'*) ;; *) continue ;; esac
      p=$(echo "$line"  | sed -n 's/.*"port":\([0-9]*\).*/\1/p')
      st=$(echo "$line" | sed -n 's/.*"status":"\([^"]*\)".*/\1/p')
      ip=$(echo "$line" | sed -n 's/.*"exit_ip":"\([^"]*\)".*/\1/p')
      hn=$(echo "$line" | sed -n 's/.*"hostname":"\([^"]*\)".*/\1/p')
      [[ -z $p ]] && continue
      printf "  %-10s%-11s%-18s%s\n" "$p" "${st:--}" "${ip:--}" "${hn:--}"
    done
  fi
  rm -f "$ck.json"
}

change_port() {
  local cur new
  cur=$(web_port)
  echo
  read -rp "  新端口 (当前 ${cur}): " new
  [[ -z $new ]] && { echo "  未修改"; return; }
  if ! [[ $new =~ ^[0-9]+$ ]] || (( new < 1 || new > 65535 )); then
    echo -e "  ${R}端口不合法${N}"; return
  fi
  if ss -tln 2>/dev/null | grep -q ":${new} "; then
    echo -e "  ${R}端口 ${new} 已被占用${N}"; return
  fi
  # 写 settings.json（权威来源），并把服务文件里可能残留的 -web 一并同步，
  # 免得老安装重启后又被写死的旧端口拽回去。
  if [[ -f "$WORK_DIR/settings.json" ]]; then
    sed -i "s/\"port\"[[:space:]]*:[[:space:]]*[0-9]*/\"port\": ${new}/" "$WORK_DIR/settings.json"
  else
    printf '{\n  "port": %s,\n  "listen_addr": ""\n}\n' "$new" > "$WORK_DIR/settings.json"
    chmod 600 "$WORK_DIR/settings.json"
  fi
  sed -i "s/-web ${cur}/-web ${new}/" "$UNIT" 2>/dev/null
  svc_reload
  svc_restart
  echo -e "  ${G}已改为 ${new} 并重启${N}"
}

reset_password() {
  local pw
  echo
  read -rp "  新口令 (留空则随机生成): " pw
  if [[ -z $pw ]]; then
    pw=$(head -c 9 /dev/urandom | od -An -tx1 | tr -d ' \n')
  fi
  umask 077
  echo "$pw" > "$WORK_DIR/password"
  svc_restart
  echo -e "  ${G}新口令: ${pw}${N}"
}

reset_basepath() {
  local bp
  echo
  read -rp "  新访问路径 (留空则随机生成): " bp
  if [[ -z $bp ]]; then
    rm -f "$WORK_DIR/basepath"
    svc_restart
    sleep 2
    bp=$(cat "$WORK_DIR/basepath" 2>/dev/null)
  else
    bp=${bp#/}; bp=${bp%/}
    umask 077
    echo "$bp" > "$WORK_DIR/basepath"
    svc_restart
  fi
  echo -e "  ${G}新路径: /${bp}/${N}"
}

ipv6_state() {
  local a d
  a=$(sysctl -n net.ipv6.conf.all.disable_ipv6 2>/dev/null || echo 0)
  d=$(sysctl -n net.ipv6.conf.default.disable_ipv6 2>/dev/null || echo 0)
  [[ "$a" == 1 && "$d" == 1 ]] && echo disabled || echo enabled
}

toggle_ipv6() {
  local conf=/etc/sysctl.d/99-fanout-ipv6.conf
  echo
  if [[ $(ipv6_state) == disabled ]]; then
    read -rp "  当前已禁用 IPv6，要重新启用吗？[y/N]: " yes
    [[ ${yes,,} == y ]] || { echo "  已取消"; return; }
    rm -f "$conf"
    sysctl -qw net.ipv6.conf.all.disable_ipv6=0
    sysctl -qw net.ipv6.conf.default.disable_ipv6=0
    sysctl -qw net.ipv6.conf.lo.disable_ipv6=0
    echo -e "  ${G}已重新启用 IPv6${N}"
    return
  fi

  echo -e "  ${D}母机有全局 IPv6 时，没走隧道的流量可能从 IPv6 出去，暴露真实地址。${N}"
  read -rp "  确认禁用整机 IPv6？[y/N]: " yes
  [[ ${yes,,} == y ]] || { echo "  已取消"; return; }

  cat > "$conf" <<EOF
net.ipv6.conf.all.disable_ipv6 = 1
net.ipv6.conf.default.disable_ipv6 = 1
net.ipv6.conf.lo.disable_ipv6 = 1
EOF
  sysctl -qw net.ipv6.conf.all.disable_ipv6=1
  sysctl -qw net.ipv6.conf.default.disable_ipv6=1
  sysctl -qw net.ipv6.conf.lo.disable_ipv6=1
  svc_restart >/dev/null 2>&1
  echo -e "  ${G}已禁用 IPv6（重启后依然生效）${N}"
}

show_links() {
  echo
  echo -e "  交流群  ${B}https://t.me/+ft-zI76oovgwNmRh${N}"
  echo -e "  油管    ${B}https://youtube.com/@joeyblog${N}"
  echo -e "  博客    ${B}https://joeyblog.net${N}"
  echo -e "  项目    ${B}https://github.com/hyp3699/fanout${N}"
  echo
  echo -e "  ${D}用着有问题、或者想要什么功能，去群里说或提 issue。${N}"
}

# 老版本把 -web 写死在服务文件里，和 settings.json 互相拽回旧值。
# 更新时把端口搬进配置再从服务文件里摘掉，之后只认一处。
migrate_port_to_settings() {
  local unit_port
  unit_port=$(grep -oE '\-web [0-9]+' "$UNIT" 2>/dev/null | grep -oE '[0-9]+' | head -1)
  [[ -z $unit_port ]] && return

  if [[ ! -f "$WORK_DIR/settings.json" ]]; then
    printf '{\n  "port": %s,\n  "listen_addr": ""\n}\n' "$unit_port" > "$WORK_DIR/settings.json"
    chmod 600 "$WORK_DIR/settings.json"
  fi
  sed -i "s/-web ${unit_port} //" "$UNIT"
  svc_reload
  echo "  已把端口 ${unit_port} 迁移到 settings.json"
}

do_update() {
  local arch goarch tmp
  arch=$(uname -m)
  case "$arch" in
    x86_64) goarch=amd64 ;;
    aarch64|arm64) goarch=arm64 ;;
    *) echo -e "  ${R}不支持的架构 ${arch}${N}"; return ;;
  esac

  echo -e "\n  当前 $("$BIN" -version 2>/dev/null || echo '-')"
  tmp=$(mktemp -d)
  echo "  正在下载最新版..."
  if ! curl -fsSL "https://github.com/${REPO}/releases/latest/download/fanout-linux-${goarch}.tar.gz" \
       -o "$tmp/f.tar.gz"; then
    echo -e "  ${R}下载失败${N}"; rm -rf "$tmp"; return
  fi
  tar xzf "$tmp/f.tar.gz" -C "$tmp"
  svc_stop
  install -m 755 "$tmp/fanout" "$BIN"
  migrate_port_to_settings
  svc_start
  rm -rf "$tmp"
  echo -e "  ${G}已更新到 $("$BIN" -version 2>/dev/null)${N}"
}

do_uninstall() {
  local yes
  echo
  read -rp "  确认卸载？隧道和配置都会删除 [y/N]: " yes
  [[ ${yes,,} == y ]] || { echo "  已取消"; return; }

  svc_stop >/dev/null 2>&1
  svc_disable
  # 清掉残留的 netns 与 veth
  for ns in $(ip netns list 2>/dev/null | awk '{print $1}' | grep '^fo[0-9]'); do
    ip netns del "$ns" 2>/dev/null
  done
  for l in $(ip -o link show 2>/dev/null | awk -F': ' '{print $2}' | grep '^fov[0-9]'); do
    ip link del "$l" 2>/dev/null
  done
  rm -f "$UNIT" "$BIN" /usr/local/bin/f
  rm -rf "$WORK_DIR"
  svc_reload
  echo -e "  ${G}已卸载${N}"
  exit 0
}

# ── 用户流量限制 / 限速（与 sb.sh 共用数据：/etc/sing-box/user_manager）────────
# 流量统计、流量限制由 sb.sh 安装的 singbox-traffic 服务负责；限速由 bandwidth-limiter 内核负责。
BASE_DIR="/etc/sing-box"
DATA_DIR="$BASE_DIR/user_manager"
LIMIT_DIR="$DATA_DIR/limits"
TRAFFIC_DIR="$DATA_DIR/traffic"
TRAFFIC_STATE="$TRAFFIC_DIR/state.json"
TRAFFIC_LIMIT_SCRIPT="$BASE_DIR/sing-box-name.sh"
TRAFFIC_LIMIT_URL="https://raw.githubusercontent.com/hyp3699/kknnuonmkk/refs/heads/main/jiao/sing-box-name.sh"
PYTHON="$(command -v python3 2>/dev/null || true)"
re='\033[0m'
skyblue='\e[1;36m'
red()    { echo -e "\e[1;91m$1\033[0m"; }
green()  { echo -e "\e[1;32m$1\033[0m"; }
yellow() { echo -e "\e[1;33m$1\033[0m"; }

format_bytes() {
    local bytes="${1:-0}"
    "$PYTHON" - "$bytes" <<'PY'
import sys
try:
    n = int(float(sys.argv[1]))
except:
    n = 0
units = ["B", "KB", "MB", "GB", "TB", "PB"]
i = 0
v = float(n)
while v >= 1024 and i < len(units) - 1:
    v /= 1024
    i += 1
if i == 0:
    print(f"{int(v)} {units[i]}")
elif v >= 100:
    print(f"{v:.0f} {units[i]}")
elif v >= 10:
    print(f"{v:.1f} {units[i]}")
else:
    print(f"{v:.2f} {units[i]}")
PY
}
get_user_traffic() {
    local user="$1"
    if [ ! -f "$TRAFFIC_STATE" ]; then
        echo "0 0 0 0 0 0 0"
        return
    fi
    "$PYTHON" - "$TRAFFIC_STATE" "$user" <<'PY'
import sys
import json
fn = sys.argv[1]
user = sys.argv[2]
try:
    with open(fn, "r", encoding="utf-8") as f:
        data = json.load(f)
except Exception:
    print("0 0 0 0 0 0 0")
    raise SystemExit
d = data.get("users", {}).get(user, {})
uplink = int(d.get("uplink", 0) or 0)
downlink = int(d.get("downlink", 0) or 0)
total = int(d.get("total", uplink + downlink) or 0)
connections = int(d.get("connections", 0) or 0)
period_uplink = int(d.get("period_uplink", 0) or 0)
period_downlink = int(d.get("period_downlink", 0) or 0)
period_total = int(d.get("period_total", period_uplink + period_downlink) or 0)
print(uplink, downlink, total, connections, period_uplink, period_downlink, period_total)
PY
}
show_limit() {
    local username="$1"
    if [ -z "$username" ]; then
        echo "流量限制：未设置        流量周期：未设置"
        echo "已用流量：未统计        流量状态：正常"
        return
    fi
    local limit_file=""
    local file
    local file_user
    for file in "$LIMIT_DIR"/*.json; do
        [ -f "$file" ] || continue
        file_user=$(jq -r '.user // empty' "$file" 2>/dev/null)
        if [ "$file_user" = "$username" ]; then
            limit_file="$file"
            break
        fi
    done
    if [ -z "$limit_file" ]; then
        echo "流量限制：未设置        流量周期：未设置"
        echo "已用流量：未统计        流量状态：正常"
        return
    fi
    local enabled
    local limit_bytes
    local period
    local disabled_by_limit
    local used=0
    enabled=$(jq -r '.enabled // false' "$limit_file" 2>/dev/null)
    limit_bytes=$(jq -r '.limit_bytes // 0' "$limit_file" 2>/dev/null)
    period=$(jq -r '.period // "none"' "$limit_file" 2>/dev/null)
    disabled_by_limit=$(jq -r '.disabled_by_limit // false' "$limit_file" 2>/dev/null)
    if [ -f "$TRAFFIC_STATE" ]; then
        used=$(jq -r --arg u "$username" '.users[$u].period_total // 0' "$TRAFFIC_STATE" 2>/dev/null)
    fi
    if ! [[ "$used" =~ ^[0-9]+$ ]]; then
        used=0
    fi
    local period_cn
    case "$period" in
        day|daily)
            period_cn="每天"
            ;;
        month|monthly)
            period_cn="每月"
            ;;
        *)
            period_cn="未设置"
            ;;
    esac
    if [ "$enabled" != "true" ] || [ "$limit_bytes" -le 0 ] 2>/dev/null; then
        echo "流量限制：未设置        流量周期：未设置"
        printf "已用流量：%-12s " "$(format_bytes "$used")"
        green "流量状态：正常"
        return
    fi
    printf "流量限制：%-12s    流量周期：%s\n" "$(format_bytes "$limit_bytes")" "$period_cn"
    printf "已用流量：%-12s    " "$(format_bytes "$used")"
    if [ "$disabled_by_limit" = "true" ]; then
        red "流量状态：已停用"
    else
        green "流量状态：正常"
    fi
}


# ================= 用户限速 (bandwidth-limiter，按用户名 name 匹配) =================
# 这段代码 sb.sh 与 fanout 的 f.sh 共用，两边读写同一份数据，改动请两边同步。
SPEED_STORE="/etc/sing-box/user_manager/speed_limits.json"
SPEED_SYNC_PY="/etc/sing-box/user_manager/speed_limit_sync.py"
SPEED_DROPIN="/etc/systemd/system/sing-box.service.d/10-speed-limit.conf"
SPEED_CONF_DIR="/etc/sing-box/conf"
SPEED_TRAFFIC_DIR="/etc/sing-box/user_manager/traffic"
SPEED_PY="$(command -v python3 2>/dev/null || true)"

speed_limit_write_sync_py() {
    mkdir -p "$(dirname "$SPEED_SYNC_PY")"
    cat > "$SPEED_SYNC_PY" <<'SPEEDPY'
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
SPEEDPY
    chmod 700 "$SPEED_SYNC_PY"
}

# 每次 sing-box 启动/重载前自动同步限速配置（sb.sh、fanout、网页分流修改路由后都会同步）
speed_limit_install_hook() {
    command -v systemctl >/dev/null 2>&1 || return 0
    [ -n "$SPEED_PY" ] || return 1
    local want
    want="[Service]
ExecStartPre=-${SPEED_PY} ${SPEED_SYNC_PY}
ExecReload=
ExecReload=-${SPEED_PY} ${SPEED_SYNC_PY}
ExecReload=/bin/kill -HUP \$MAINPID"
    if [ ! -f "$SPEED_DROPIN" ] || [ "$(cat "$SPEED_DROPIN")" != "$want" ]; then
        mkdir -p "$(dirname "$SPEED_DROPIN")"
        printf '%s\n' "$want" > "$SPEED_DROPIN"
        systemctl daemon-reload
    fi
}

speed_limit_sync() {
    [ -n "$SPEED_PY" ] || { echo "未找到 python3"; return 1; }
    speed_limit_write_sync_py
    speed_limit_install_hook
    "$SPEED_PY" "$SPEED_SYNC_PY"
}

# 输出: speed|mode ，未设置输出空
speed_limit_get() {
    [ -f "$SPEED_STORE" ] || return 0
    jq -r --arg u "$1" '.users[$u] // empty | "\(.speed)|\(.mode // "bidirectional")"' "$SPEED_STORE" 2>/dev/null
}

# 把 10MB / 500KB / 100Mbps 换算成 字节/秒（sing-box 规则：MB=1000*1000，Mbps=MB/8）
speed_limit_to_bps() {
    awk -v s="$1" 'BEGIN{
        if (match(s, /^[0-9]+/) == 0) { print 0; exit }
        n = substr(s, 1, RLENGTH) + 0; u = substr(s, RLENGTH + 1)
        if (u == "Kbps") v = n * 1000 / 8
        else if (u == "Mbps") v = n * 1000000 / 8
        else if (u == "Gbps") v = n * 1000000000 / 8
        else { u = tolower(u)
            if (u == "k" || u == "kb" || u == "kbps") v = n * 1000
            else if (u == "m" || u == "mb" || u == "mbps") v = n * 1000000
            else if (u == "g" || u == "gb" || u == "gbps") v = n * 1000000000
            else v = n }
        printf "%d", v }'
}

# 字节/秒 → "1.00 MB/s（8.0 Mbps）"
speed_rate_text() {
    awk -v b="${1:-0}" 'BEGIN{
        if (b >= 1000000) s = sprintf("%.2f MB/s", b / 1000000)
        else if (b >= 1000) s = sprintf("%.1f KB/s", b / 1000)
        else s = sprintf("%d B/s", b)
        m = b * 8 / 1000000
        if (m >= 10) t = sprintf("%.0f Mbps", m); else if (m >= 0.1) t = sprintf("%.1f Mbps", m); else t = sprintf("%.0f Kbps", b * 8 / 1000)
        printf "%s（%s）", s, t }'
}

speed_limit_text() {
    local info speed mode mode_cn
    info="$(speed_limit_get "$1")"
    if [ -z "$info" ]; then
        echo "未设置"
        return
    fi
    IFS='|' read -r speed mode <<< "$info"
    case "$mode" in
        upload) mode_cn="仅上传" ;;
        download) mode_cn="仅下载" ;;
        *) mode_cn="上传+下载" ;;
    esac
    echo "$(speed_rate_text "$(speed_limit_to_bps "$speed")") ${mode_cn}"
}

show_speed_limit() {
    local text
    text="$(speed_limit_text "$1")"
    if [ "$text" = "未设置" ]; then
        echo "当前限速：未设置"
    else
        echo -e "当前限速：\e[1;33m${text}\033[0m"
    fi
}

# 读取用户累计流量（v2ray_api 统计），输出 "上传 下载"
speed_user_counters() {
    local g="$SPEED_TRAFFIC_DIR/grpcurl"
    [ -x "$g" ] && [ -f "$SPEED_TRAFFIC_DIR/stats.proto" ] || return 1
    local out
    out=$("$g" -plaintext -max-time 3 -import-path "$SPEED_TRAFFIC_DIR" -proto "$SPEED_TRAFFIC_DIR/stats.proto" \
        -d "$(jq -nc --arg p "user>>>$1>>>traffic>>>" '{pattern:$p,reset:false}')" \
        127.0.0.1:9094 v2ray.core.app.stats.command.StatsService/QueryStats 2>/dev/null) || return 1
    echo "$out" | jq -r '(.stat // []) as $s
        | [([$s[] | select(.name | endswith(">>>uplink")) | (.value // "0" | tonumber)] | add // 0),
           ([$s[] | select(.name | endswith(">>>downlink")) | (.value // "0" | tonumber)] | add // 0)] | @tsv' 2>/dev/null
}

# 实时网速：间隔 2 秒取两次统计
show_live_speed() {
    local a b u1 d1 u2 d2
    a="$(speed_user_counters "$1")" || { echo "实时网速：未统计"; return; }
    sleep 2
    b="$(speed_user_counters "$1")" || { echo "实时网速：未统计"; return; }
    read -r u1 d1 <<< "$a"
    read -r u2 d2 <<< "$b"
    local up=$(( (${u2:-0} - ${u1:-0}) / 2 )) down=$(( (${d2:-0} - ${d1:-0}) / 2 ))
    [ "$up" -lt 0 ] && up=0
    [ "$down" -lt 0 ] && down=0
    echo -e "实时网速：上传 \e[1;36m$(speed_rate_text "$up")\033[0m  下载 \e[1;36m$(speed_rate_text "$down")\033[0m"
}

# 统计该用户名已有的分流规则数量
speed_limit_rule_count() {
    "$SPEED_PY" - "$SPEED_CONF_DIR" "$1" <<'PY'
import glob, json, os, sys
d, name = sys.argv[1], sys.argv[2]
n = 0
for fn in glob.glob(os.path.join(d, "*.json")):
    if os.path.basename(fn) == "00-limiter.json":
        continue
    try:
        data = json.load(open(fn, encoding="utf-8"))
    except Exception:
        continue
    for r in (data.get("route") or {}).get("rules", []) or []:
        if isinstance(r, dict) and r.get("action", "route") == "route" and name in (r.get("auth_user") or []):
            n += 1
print(n)
PY
}

speed_limit_store_set() {
    local name="$1" speed="$2" mode="$3" tmp
    mkdir -p "$(dirname "$SPEED_STORE")"
    [ -s "$SPEED_STORE" ] || echo '{"users":{}}' > "$SPEED_STORE"
    tmp="$(mktemp)"
    if [ -z "$speed" ]; then
        jq --arg u "$name" 'del(.users[$u])' "$SPEED_STORE" > "$tmp"
    else
        jq --arg u "$name" --arg s "$speed" --arg m "$mode" '.users[$u] = {"speed": $s, "mode": $m}' "$SPEED_STORE" > "$tmp"
    fi
    mv -f "$tmp" "$SPEED_STORE"
    chmod 600 "$SPEED_STORE"
}

speed_limit_apply() {
    local name="$1" old_info="$2" old_speed old_mode err
    if ! err="$(speed_limit_sync 2>&1)"; then
        if [ -n "$old_info" ]; then
            IFS='|' read -r old_speed old_mode <<< "$old_info"
            speed_limit_store_set "$name" "$old_speed" "$old_mode"
        else
            speed_limit_store_set "$name" "" ""
        fi
        speed_limit_sync >/dev/null 2>&1
        echo -e "\e[1;91m限速配置检查失败，已恢复原设置：\033[0m"
        echo "$err" | tail -n 3
        echo -e "\e[1;33m如提示 unknown outbound type: bandwidth-limiter，请先更新为 -xhttp-limiter 内核\033[0m"
        read -rp "按回车返回..." _
        return 1
    fi
    systemctl reload sing-box
    return 0
}

speed_limit_input() {
    local input mode_choice kb
    echo "单位换算：1 MB/s = 8 Mbps，10 MB/s = 80 Mbps"
    while true; do
        read -rp "请输入限速（默认单位 MB/s，例如 10 或 0.5；也可写 500KB、100Mbps）: " input
        input="$(echo "$input" | tr -d '[:space:]')"
        if [[ "$input" =~ ^[0-9]+$ ]] && [ "$input" -gt 0 ]; then
            new_speed="${input}MB"; break
        elif [[ "$input" =~ ^[0-9]*\.[0-9]+$ ]]; then
            kb=$(awk -v v="$input" 'BEGIN{printf "%d", v*1000}')
            if [ "$kb" -gt 0 ]; then new_speed="${kb}KB"; break; fi
        elif [[ "$input" =~ ^([0-9]+)([Mm][Bb][Pp][Ss]|[Kk][Bb][Pp][Ss]|[Gg][Bb][Pp][Ss])$ ]] && [ "${BASH_REMATCH[1]}" -gt 0 ]; then
            local u="${BASH_REMATCH[2]}"
            case "${u,,}" in
                kbps) u="Kbps" ;;
                mbps) u="Mbps" ;;
                gbps) u="Gbps" ;;
            esac
            new_speed="${BASH_REMATCH[1]}${u}"; break
        elif [[ "$input" =~ ^([0-9]+)([Kk][Bb]?|[Mm][Bb]?|[Gg][Bb]?)(/[Ss])?$ ]] && [ "${BASH_REMATCH[1]}" -gt 0 ]; then
            local u="${BASH_REMATCH[2]^^}"
            [ "${#u}" -eq 1 ] && u="${u}B"
            new_speed="${BASH_REMATCH[1]}${u}"; break
        fi
        echo -e "\e[1;91m输入无效，请重新输入\033[0m"
    done
    echo "将限速为：$(speed_rate_text "$(speed_limit_to_bps "$new_speed")")"
    echo
    echo -e "\e[1;32m限速方向：\033[0m"
    echo "  1. 上传+下载（默认）"
    echo "  2. 仅上传"
    echo "  3. 仅下载"
    read -rp "请选择 [1]: " mode_choice
    case "$mode_choice" in
        2) new_mode="upload" ;;
        3) new_mode="download" ;;
        *) new_mode="bidirectional" ;;
    esac
}

speed_limit_menu() {
    local name="${1:-}"
    [ -n "$name" ] || { echo "用户名为空"; sleep 1; return; }
    local choice="" old_info="" new_speed="" new_mode="" rule_count=0
    while true; do
        clear
        echo -e "\e[1;32m================ 限速管理 ================\033[0m"
        echo
        echo -e "\e[1;32m用户：${name}\033[0m"
        show_speed_limit "$name"
        show_live_speed "$name"
        rule_count="$(speed_limit_rule_count "$name" 2>/dev/null)"
        [[ "$rule_count" =~ ^[0-9]+$ ]] || rule_count=0
        if [ "$rule_count" -gt 0 ]; then
            echo "分流规则：${rule_count} 条（限速后仍按分流规则出站）"
        else
            echo "分流规则：无"
        fi
        echo -e "\e[1;32m------------------------------------------\033[0m"
        echo -e "\e[1;32m1. 设置限速\033[0m"
        echo -e "\e[1;32m2. 修改限速\033[0m"
        echo -e "\e[1;91m3. 取消限速\033[0m"
        echo -e "\e[1;32m4. 刷新实时网速\033[0m"
        echo
        echo -e "\e[1;32m------------------------------------------\033[0m"
        echo -e "\e[1;32m0. 返回\033[0m"
        echo
        read -rp "请选择: " choice
        old_info="$(speed_limit_get "$name")"
        case "$choice" in
            1|2)
                if [ "$choice" = "1" ] && [ -n "$old_info" ]; then
                    echo -e "\e[1;33m已设置限速：$(speed_limit_text "$name")，将覆盖\033[0m"
                elif [ "$choice" = "2" ] && [ -z "$old_info" ]; then
                    echo -e "\e[1;33m当前未设置限速，请先选择 1 设置限速\033[0m"
                    sleep 1.5
                    continue
                fi
                new_speed=""; new_mode=""
                speed_limit_input
                speed_limit_store_set "$name" "$new_speed" "$new_mode"
                if speed_limit_apply "$name" "$old_info"; then
                    echo -e "\n\e[1;32m已设置：$(speed_limit_text "$name")\033[0m"
                    [ "$rule_count" -gt 0 ] && echo -e "\e[1;32m该用户的 ${rule_count} 条分流规则已同步到限速出站，同时生效\033[0m"
                    sleep 2
                fi
                ;;
            3)
                if [ -z "$old_info" ]; then
                    echo -e "\e[1;33m当前未设置限速\033[0m"
                    sleep 1
                    continue
                fi
                speed_limit_store_set "$name" "" ""
                if speed_limit_apply "$name" "$old_info"; then
                    echo -e "\e[1;32m已取消 ${name} 的限速\033[0m"
                    sleep 1.5
                fi
                ;;
            4) continue ;;
            0) return ;;
            *) echo -e "\e[1;91m无效选项\033[0m"; sleep 1 ;;
        esac
    done
}


# 列出配置目录里所有入站中的用户名：每行 "用户名|入站1,入站2"
list_singbox_users() {
  "$PYTHON" - "$SPEED_CONF_DIR" <<'PY'
import glob, json, os, sys
order, where = [], {}
for fn in sorted(glob.glob(os.path.join(sys.argv[1], "*.json"))):
    try:
        data = json.load(open(fn, encoding="utf-8"))
    except Exception:
        continue
    for inb in data.get("inbounds", []) or []:
        if not isinstance(inb, dict):
            continue
        tag = inb.get("tag", "")
        for u in inb.get("users", []) or []:
            if not isinstance(u, dict):
                continue
            n = u.get("name") or u.get("username")
            if not n:
                continue
            if n not in where:
                where[n] = []
                order.append(n)
            if tag and tag not in where[n]:
                where[n].append(tag)
for n in order:
    print(n + "|" + ",".join(where[n]))
PY
}

traffic_limit_edit() {
  local name="$1"
  if [[ ! -f "$TRAFFIC_LIMIT_SCRIPT" ]]; then
    echo "  正在下载流量限制脚本..."
    curl -fsSL "$TRAFFIC_LIMIT_URL" -o "$TRAFFIC_LIMIT_SCRIPT.tmp" && [[ -s "$TRAFFIC_LIMIT_SCRIPT.tmp" ]] \
      && mv -f "$TRAFFIC_LIMIT_SCRIPT.tmp" "$TRAFFIC_LIMIT_SCRIPT" \
      || { rm -f "$TRAFFIC_LIMIT_SCRIPT.tmp"; red "  下载失败"; pause; return; }
  fi
  if ! systemctl list-unit-files singbox-traffic.service >/dev/null 2>&1 || \
     ! systemctl cat singbox-traffic.service >/dev/null 2>&1; then
    yellow "  未检测到流量统计服务 singbox-traffic，流量限制需要先用 sb.sh 安装"
    pause
    return
  fi
  systemctl is-active --quiet singbox-traffic.service || systemctl start singbox-traffic.service >/dev/null 2>&1 || true
  bash "$TRAFFIC_LIMIT_SCRIPT" "$name"
}

user_limit_detail() {
  local name="$1" c traffic uplink downlink total period_total
  while true; do
    clear
    green "================ 用户管理 ================"
    echo
    green "用户：${name}"
    echo
    echo -e "${skyblue}流量统计${re}"
    uplink=""; downlink=""; total=""; period_total=""
    if [[ -f "$TRAFFIC_STATE" ]]; then
      traffic="$(get_user_traffic "$name" 2>/dev/null)"
      read -r uplink downlink total _ _ _ period_total <<< "$traffic"
    fi
    if [[ -n "$total" ]]; then
      printf "上传：%-18s 总流量：%s\n" "$(format_bytes "$uplink")" "$(format_bytes "$total")"
      printf "下载：%-18s 本周期：%s\n" "$(format_bytes "$downlink")" "$(format_bytes "$period_total")"
    else
      echo "上传：未统计          总流量：未统计"
      echo "下载：未统计          本周期：未统计"
    fi
    echo -e "${skyblue}流量限制${re}"
    show_limit "$name"
    echo -e "${skyblue}限速${re}"
    show_speed_limit "$name"
    green "------------------------------------------"
    green "1. 流量限制"
    green "2. 限速管理"
    echo
    green "------------------------------------------"
    green "0. 返回"
    echo
    read -rp "请选择: " c
    case "$c" in
      1) traffic_limit_edit "$name" ;;
      2) speed_limit_menu "$name" ;;
      0) return ;;
      *) ;;
    esac
  done
}

user_limit_menu() {
  local lines name tags idx c users=() traffic total
  if [[ -z "$PYTHON" ]] || ! command -v jq >/dev/null 2>&1; then
    red "  需要 python3 和 jq（sb.sh 会自动安装）"; pause; return
  fi
  while true; do
    clear
    echo -e "${B}  用户流量限制 / 限速${N}  ${D}按入站里的用户名 name 生效，与 sb.sh 共用设置${N}"
    echo
    lines="$(list_singbox_users)"
    users=()
    idx=1
    if [[ -z "$lines" ]]; then
      echo "  配置目录里的入站还没有用户"
    else
      while IFS='|' read -r name tags; do
        [[ -n "$name" ]] || continue
        users+=("$name")
        total=""
        if [[ -f "$TRAFFIC_STATE" ]]; then
          traffic="$(get_user_traffic "$name" 2>/dev/null)"
          read -r _ _ total _ _ _ _ <<< "$traffic"
        fi
        printf "  %2d) %-16s ${D}%-22s${N} 已用 %-10s 限速 %s\n" "$idx" "$name" "$tags" \
          "$(format_bytes "${total:-0}")" "$(speed_limit_text "$name")"
        idx=$((idx + 1))
      done <<< "$lines"
    fi
    echo
    echo "   0) 返回"
    read -rp "  选择用户: " c
    [[ "$c" == "0" || -z "$c" ]] && return
    if [[ "$c" =~ ^[0-9]+$ ]] && (( c >= 1 && c <= ${#users[@]} )); then
      user_limit_detail "${users[$((c - 1))]}"
    fi
  done
}


menu() {
  while true; do
    clear
    echo -e "${B}  fanout${N}  ${D}VPN Gate 出口扇出网关${N}"
    show_info
    echo -e "${D}  ─────────────────────────────${N}"
    echo "   1) 启动          2) 停止"
    echo "   3) 重启          4) 查看日志"
    echo
    echo "   5) 隧道列表      6) 连接信息"
    echo
    echo "   7) 改端口        8) 改口令"
    echo "   9) 改访问路径   10) 开机自启开关"
    echo
    echo "  11) 更新         12) 卸载"
    echo "  13) 交流群 / 反馈"
    echo "  14) 用户流量限制 / 限速"
    echo "   0) 退出"
    echo -e "${D}  ─────────────────────────────${N}"
    read -rp "  选择: " choice

    case "$choice" in
      1) svc_start   && echo -e "\n  ${G}已启动${N}"; pause ;;
      2) svc_stop    && echo -e "\n  ${Y}已停止${N}"; pause ;;
      3) svc_restart && echo -e "\n  ${G}已重启${N}"; pause ;;
      4) echo; svc_logs 40; pause ;;
      5) list_tunnels; pause ;;
      6) show_info; pause ;;
      7) change_port; pause ;;
      8) reset_password; pause ;;
      9) reset_basepath; pause ;;
      10)
        if svc_is_enabled; then
          svc_disable
          echo -e "\n  ${Y}已关闭开机自启${N}"
        else
          svc_enable
          echo -e "\n  ${G}已开启开机自启${N}"
        fi
        pause ;;
      11) do_update; pause ;;
      13) show_links; pause ;;
      14) user_limit_menu ;;
      12) do_uninstall; pause ;;
      0) exit 0 ;;
      *) ;;
    esac
  done
}

need_root

# 带参数时当普通命令用，不进菜单
case "${1:-}" in
  start)    svc_start ;;
  stop)     svc_stop ;;
  restart)  svc_restart ;;
  status)   svc_status_page ;;
  log)      svc_logs_follow ;;
  info)     show_info ;;
  list)     list_tunnels ;;
  update)   do_update ;;
  uninstall) do_uninstall ;;
  limit)    user_limit_menu ;;
  "")       menu ;;
  *)
    echo "用法: f [start|stop|restart|status|log|info|list|update|uninstall|limit]"
    echo "不带参数进入交互菜单"
    ;;
esac
