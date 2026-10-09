# fanout

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

把 VPN Gate 的公共节点变成本地 SOCKS5 端口：一个端口一个出口 IP。
再按**分流规则**把同机 [sing-box](https://github.com/SagerNet/sing-box) 入站里的部分流量
（指定的域名、geosite / geoip 规则集）送进这些出口，比如只让奈飞走日本家宽，其余照旧直连。

fanout 不建入站：入站由同机别的脚本（比如 sb.sh）管理，fanout 只读取它们、显示分享链接，
并在自己的 `fanout-route.json` 里写分流规则。

![主界面](https://images.joeyblog.net/2026/7/27/fanout-dashboard.png)

四条隧道跑在一台机器上，四个端口对应四个国家的出口，母机自己的 IP 不受影响：

![出口验证](https://images.joeyblog.net/2026/7/26/fanout-6-exit-ip.png)

## 原理

每个节点跑在独立的 network namespace 里，netns 内启动官方 openvpn 客户端。
SOCKS5 监听在母机，出站连接用 `setns` 切进对应 netns 建立。

这样做的好处：VPN 的路由劫持只影响自己的 netns，不会切断母机的网络；
多个节点互不干扰，各自一个出口 IP。

```
客户端 ──> 母机 SOCKS5 :随机端口 ──> netns foN ──> openvpn ──> VPN Gate 节点
```

## 安装

需要 root，Linux（依赖 netns）。

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/hyp3699/fanout/main/install.sh)
```

会自动下载对应架构的预编译二进制。也可以 clone 仓库后在源码目录运行同一个脚本，
那样会从源码编译（需要 Go 1.21+）。

依赖（openvpn / curl / tar / iproute / iptables）会按发行版自动装，
apt、dnf、yum、pacman、apk、zypper 都认。

sing-box 的位置：

- 二进制 `/etc/sing-box/sing-box`，已经有了（比如 sb.sh 装过）就直接用，不会重新下载；
  没有才从 [SagerNet/sing-box](https://github.com/SagerNet/sing-box/releases) 下最新版（按架构，Alpine 用 musl 包）。
- 配置目录 `/etc/sing-box/conf/`，sing-box 以 `sing-box run -C /etc/sing-box/conf` 加载整个目录。
- 换位置用环境变量：`SINGBOX_DIR=/opt/sing-box SINGBOX_CONF=/opt/sing-box/conf bash install.sh`，
  也可以指定版本 `SINGBOX_VERSION=1.14.2`。

服务用 systemd 或 OpenRC 都能装，装完自动开机自启。

**Alpine** 默认不带 bash，先装一下：

```bash
apk add bash curl
bash <(curl -fsSL https://raw.githubusercontent.com/hyp3699/fanout/main/install.sh)
```

另外 fanout 要在 netns 里跑 openvpn，**宿主必须放开 `/dev/net/tun`**。
不少 LXC 小鸡没给这个权限，`ls /dev/net/tun` 不存在且 `mknod` 报
Operation not permitted 的话，这台机器用不了，跟发行版无关。

装完敲 `f` 打开管理菜单：

![管理菜单](https://images.joeyblog.net/2026/7/26/fanout-7-menu.png)

装完会打印管理界面地址、访问路径和口令：

```
管理界面  http://<你的IP>:8899/gwPuWHvaNr/
访问口令  f81120ac328d11c11b
```

路径和口令都是随机生成的，分别存在 `/var/lib/fanout/basepath` 和
`/var/lib/fanout/password`。路径不对一律返回 404，扫端口的看不到这里跑着什么。

## 使用

界面分三块：**出口**、**分流规则**、**入站**（只读）。

### 出口

点「新建出口」，选地区和数量，fanout 并行拉起隧道，进度按目标逐条回报。
新建出口只开隧道，**不会建任何入站**；想让哪些流量走新出口，去「分流规则」里建规则。

地区里还有个「每个国家」：每个有空闲节点的国家各开几条，一次把所有地区铺开。
槽位不够时先开节点多的地区，不会中途报错。

每行右侧两个按钮：换一个节点（出口 IP 变、SOCKS5 端口不变，指向它的分流规则自动跟过去），
或者停掉这个出口。换节点会避开这条出口之前用过的，连点几次每次都是新 IP。
出口这一行还列着指向它的分流规则，点一下就能编辑。

出口本身就是一个 SOCKS5 代理（锁形按钮看/改用户名口令），不经过 sing-box 也能直接用。

### 分流规则

一条规则 = **一个或多个入站** + **匹配条件** → **一个目标**：

```
入站匹配 && (任一自定义域名 || 任一规则集) → fanout-exit-<出口> 或 已有出站
```

- **目标**：一个下拉框，分两组：
  - 「fanout 出口」：fanout 开的 VPN Gate 出口（`fanout-exit-<节点>`），显示为「国旗 国名 节点完整 IP」；
  - 「已有出站」：配置目录里别的文件（`outbounds.json`、`endpoints.json` 等）定义的出站和端点，
    比如 `direct`、warp 的 socks、`wireguard-out` 端点。`block` / `dns` 这类特殊出站不列出。
    指向已有出站的规则**始终生效**，不受隧道连没连通影响；那个出站被别的脚本删掉后规则自动跳过（日志里写明）。

- **自定义域名**：文本框一行一个（逗号、空格分隔也行）。
  - `netflix.com` → `domain_suffix`（它自己和所有子域）；`*.netflix.com` 或 `.netflix.com` 只匹配子域
  - `full:www.netflix.com` → `domain`（只匹配这个域名）
  - `keyword:nflx` → `domain_keyword`
  - `regex:^.+\.nflxvideo\.net$` → `domain_regex`
  - `203.0.113.0/24`、`1.2.3.4`（或 `ip:` 前缀）→ `ip_cidr`
  - 误填进来的 `geosite:xxx` / `geoip:xxx` / `.srs` `.json` 地址会自动当成规则集
- **已有规则集**：配置目录里别的文件（通常是 `route.json`）已经定义的 `route.rule_set`，
  界面列出它们的 tag 供多选，规则里直接按 tag 引用，**不重复定义、也不下载**。
  被别的脚本删掉的 tag 生成配置时自动略过（日志里写明，界面划线）；全删光且没有别的条件时这条规则不生效。
  名字（tag / 地址 / 路径）里带 `geoip` 的当作 IP 规则集，自动先解析再匹配；别的看不出来的有 IP 段时勾「规则集里有 IP 段」。
- **自定义规则集**：可以加多个。
  - `geosite:netflix`、`geoip:jp` 自动展开成 SagerNet 官方
    [sing-geosite](https://github.com/SagerNet/sing-geosite/tree/rule-set) /
    [sing-geoip](https://github.com/SagerNet/sing-geoip/tree/rule-set) 的 `.srs` 地址
  - 也可以填任意远程地址：`.srs` 是 `binary`，`.json` 是 `source`，看不出来就手动选格式
  - 保存时 fanout 先下载一次校验（404、不是规则集文件当场报错），之后 sing-box 每天自己更新
- **全部流量**：要把整条入站都送到目标，必须显式勾选「全部流量」（此时不能再填条件）。
  条件留空又没勾，保存会被拒绝——fanout 绝不会因为没填条件就把整条入站带走。
- 规则按列表顺序匹配，先命中先生效，可以上移下移、停用、编辑、删除。
- 没命中任何规则的流量保持原来的走向（`route.json` 的 `final`，或别的脚本自己的规则）。
- **出口没连通**（正在连接、失败、已停掉）时，指向它的规则暂时不写进配置，命中的流量按原路由走；
  出口连通后自动写回。出口停掉后规则还在，界面标红，编辑换个出口即可。
- 规则里引用的入站被别的脚本删掉了，规则会自动略过它（全删光了这条规则就不生效），界面上划线提示。

### 入站（只读）

配置目录里所有带 tag 的入站（anytls、tuic、hysteria2、vless、vmess、trojan、shadowsocks……）
都会列出来，点开看协议、用户、被哪些规则引用，以及分享链接。端口、用户、增删都归原脚本管，
fanout 不改。分享链接优先读 `/etc/sing-box/url/*.txt` 里脚本自己导出的（按端口匹配，只读），
没有再由 fanout 推（anytls:// tuic:// hysteria2:// ss:// vless:// vmess:// trojan://）。

「导出链接」一次性拿到所有入站的分享链接：

![导出链接](https://images.joeyblog.net/2026/7/27/fanout-export.png)

### 只用家宽

VPN Gate 的清单里混着一批它自己的机房服务器（`public-vpn-*` 和
`219.100.37.0/24`），出口一眼看得出是数据中心，还更容易满员。
设置里「只用家宽节点」默认开着，挑节点、地区可用数、自动重连的备选都只看
志愿者家宽。想连机房的一起用就把它关掉。

开关只管新挑的节点，已经跑着的出口不会因为改设置被换掉。

### sing-box 配置怎么写

fanout 只管 sing-box 配置目录里的**两个文件**，目录里别的文件（`config.json`、`route.json`、
`outbounds.json`、`endpoints.json`、`anytls-1.json`、`tuic-1.json` 这些）一律只读不写：

| 文件 | 内容 |
| --- | --- |
| `fanout-outbounds.json` | 每条**连通**出口一个 `socks` 出站 `fanout-exit-<节点>`，指向 `127.0.0.1:<该出口的 SOCKS5 端口>`；一条出口都没连通时是 `{"outbounds": []}` |
| `fanout-route.json` | `route.rules`（分流规则）+ `route.rule_set`（fanout 自己的规则集）；别的文件写了 `final` 时不写 `final` |

**直连不再自建**：fanout 需要直连出站的地方（目前只有 sing-box 早于 1.14 时规则集的 `download_detour`）
复用配置目录里已有的 direct 出站——优先 tag 就叫 `direct` 的，否则按合并顺序第一个 `type: direct` 的。
只有整个配置目录里一个 direct 出站都没有时，才在 `fanout-outbounds.json` 里兜底建一个 `fanout-direct`。


规则存在 `/var/lib/fanout/native.json`，每次改动整份重新生成这两个文件。
一条「入站 anytls-1、tuic-1 + 域名 netflix.com / full:www.example.com + geosite:netflix → 日本出口」
加一条「入站 anytls-1 + geoip:jp → 日本出口」，再加一条「入站 tuic-1 + route.json 里已有的规则集 `geosite-cn` → 已有出站 `direct`」
生成的 `fanout-route.json` 大致是：

```json
{
  "route": {
    "rules": [
      {"inbound": ["anytls-1", "tuic-1"], "action": "sniff"},
      {"inbound": ["anytls-1", "tuic-1"], "domain_suffix": ["netflix.com"], "domain": ["www.example.com"],
       "action": "route", "outbound": "fanout-exit-vpn123"},
      {"inbound": ["anytls-1", "tuic-1"], "rule_set": ["fanout-rs-geosite-netflix"],
       "action": "route", "outbound": "fanout-exit-vpn123"},
      {"inbound": ["anytls-1"], "rule_set": ["fanout-rs-geoip-jp"], "action": "route", "outbound": "fanout-exit-vpn123"},
      {"inbound": ["anytls-1"], "action": "resolve", "strategy": "prefer_ipv4"},
      {"inbound": ["anytls-1"], "rule_set": ["fanout-rs-geoip-jp"], "action": "route", "outbound": "fanout-exit-vpn123"},
      {"inbound": ["tuic-1"], "rule_set": ["geosite-cn"], "action": "route", "outbound": "direct"}
    ],
    "rule_set": [
      {"type": "remote", "tag": "fanout-rs-geosite-netflix", "format": "binary",
       "url": "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-netflix.srs",
       "http_client": {"connect_timeout": "30s"}, "update_interval": "1d",
       "initial_path": "/etc/sing-box/fanout-rulesets/fanout-rs-geosite-netflix.srs"},
      {"type": "remote", "tag": "fanout-rs-geoip-jp", "format": "binary",
       "url": "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-jp.srs",
       "http_client": {"connect_timeout": "30s"}, "update_interval": "1d",
       "initial_path": "/etc/sing-box/fanout-rulesets/fanout-rs-geoip-jp.srs"}
    ]
  }
}
```

几点说明：

- **嗅探**：按域名分流要先拿到 SNI / Host。sing-box 1.11 起嗅探是路由动作，fanout 在最前面给
  涉及的入站加一条 `{"action":"sniff"}`。它不改目标地址，对已经嗅探过的连接再嗅一次也没有副作用，
  不影响别的脚本的规则。勾了「全部流量」的规则不需要嗅探。
- **域名和规则集分成两条规则**写，送到同一个出口：同一条规则里规则集是否与域名字段"合并"
  取决于规则集本身的形状，分开写语义最清楚。
- **解析**：目标是域名时 `ip_cidr`、`geoip` 匹配不到，所以规则里有 IP/CIDR 或 `geoip:` 时
  （或自定义规则集勾了「有 IP 段」），会在这条规则后面插一条只作用于这些入站的
  `resolve`（`prefer_ipv4`），再匹配一遍 IP 类条件。代价是这些入站里没命中前面规则的流量
  会先被解析一次（之后照常落到原路由）；只用域名 / geosite 的规则不会触发。
- **规则集下载**：sing-box 1.14 起 `download_detour` 已废弃，fanout 写的是内联
  `http_client`（不带 detour，从本机直接下载）；还会把保存时预下载好的文件放在
  `/etc/sing-box/fanout-rulesets/` 并写进 `initial_path`——sing-box 启动时还没有缓存、
  GitHub 又连不上，也能用这份本地文件起来，不会因为下载失败整个服务起不来。
  检测到 sing-box 早于 1.14 时退回 `download_detour: <已有的 direct 出站>`（没有 `initial_path`）。
- 规则集 tag 一律 `fanout-rs-` 开头，同一个地址在多条规则里只定义一次；万一跟别的脚本已有的
  `rule_set` tag 撞名会自动加后缀。

**规则合并顺序**：`sing-box run -C` 把目录里的 `.json` 按文件名排序后逐个合并，
对象逐键合并、数组按顺序追加。`fanout-route.json` 排在 `route.json` 前面，
所以 fanout 的分流规则排在最前、先匹配，不会被别的脚本的规则或 `final` 吞掉；
`fanout-outbounds.json` 排在 `outbounds.json` 前面，合并后第一个出站会是某条 `fanout-exit-*`。
目录里没人设 `route.final` 时 sing-box 会把第一个出站当默认，所以这种情况下 fanout 在
`fanout-route.json` 里显式写 `final`，值取"没有 fanout 时本来的默认出站"（别的文件里合并顺序第一个出站；
一个都没有时就是兜底的 `fanout-direct`），整机的默认走向不变，不会变成某条隧道。别的文件写了 `final`
（比如 sb.sh 的 `route.json` 里 `"final": "direct"`）时 fanout 不写。
如果有名字排在 `fanout-route.json` 前面（比如 `00-xxx.json`、`config.json`）的文件里也写了
能匹配同一入站的规则，它会抢先——fanout 启动时会在日志里提示这种文件。

**从老版本升级**：

- 老版本「整条入站绑到出口」的绑定**全部丢弃**（启动日志里逐条写明丢了哪些），
  不会悄悄保留成整条转发；需要的话在「分流规则」里重建（可以勾「全部流量」）。
- 老版本 fanout 自建的入站文件 `fanout-in-*.json` 原样留在配置目录里，当作普通的已有入站只读显示
  （界面编号不变），fanout 不再改写或删除它们；不要了就自己删文件。
- 订阅功能已移除，`settings.json` 里旧的 `sub_token` 下次保存设置时自动消失。
- 老版本 `fanout-outbounds.json` 里的 `fanout-direct` 在下一次重写时自动去掉（目录里有 direct 出站的话）；
  已有规则的数据格式不变，不需要迁移。

**生效方式**：每次改动先写文件，再跑 `sing-box check -C /etc/sing-box/conf`（整目录一起校验），
不过就把 fanout 的两个文件回滚、操作报错，绝不把坏配置留给 sing-box。校验通过后：

- 有 `sing-box.service`（systemd）或 `/etc/init.d/sing-box`（OpenRC）：`reload`（即 HUP），失败再 `restart`。
  这时 fanout 不会自己再起一个 sing-box。两个文件内容都没变时不重载，免得白白打断别的脚本的连接。
- 没有服务：fanout 自己托管 `sing-box run -C /etc/sing-box/conf` 子进程，退出时一起停掉。
- 配置里有规则集时，重载后再等几秒确认 sing-box 还活着（`check` 不下载规则集，下载问题要到启动时才暴露），
  没起来就回滚 fanout 的文件并再重载一次。

启动参数（默认值即上面的布局）：

```
-singbox-bin      /etc/sing-box/sing-box
-singbox-conf     /etc/sing-box/conf
-singbox-service  sing-box        # 填 none 则不用服务、由 fanout 托管进程
```

出口的 SOCKS5 端口随机分配，避开正在监听的端口以及 40000（warp socks）、9093、9094。

管理界面用到的接口（都要登录）：

| 接口 | 说明 |
| --- | --- |
| `GET /api/exits` | 出口 + 入站 + 分流规则 + 已有出站（`outbounds`）+ 已有规则集（`rule_sets`），界面一次拿全 |
| `GET /api/inbounds`、`/api/inbounds/detail?id=`、`/api/inbounds/links?ids=` | 入站列表 / 详情 / 分享链接（只读） |
| `GET /api/rules` | 分流规则（带出口状态、是否生效） |
| `POST /api/rules/save` | 新建或修改规则，JSON：`{"id":0,"name":"","enabled":true,"inbounds":["anytls-1"],"exit":"<节点主机名>","domains":"netflix.com\nfull:a.com","rule_sets":[{"source":"geosite:netflix","format":""}],"local_rule_sets":["geosite-cn"],"all":false,"resolve_ip":false}`；`exit` 与 `outbound`（已有出站 tag，如 `"direct"`）二选一 |
| `POST /api/rules/delete?id=`、`/api/rules/move?id=&dir=up\|down`、`/api/rules/enable?id=&on=1\|0` | 删除 / 调顺序 / 启停 |
| `POST /api/provision?count=&region=`（或 `every=1`） | 批量开出口（只开隧道） |

## 运维

装完后敲 `f` 打开管理菜单：启停、看日志、查隧道、改端口/口令/访问路径、更新、卸载。

```
  状态      运行中
  版本      fanout v0.1.1
  开机自启  enabled

  管理地址  http://1.2.3.4:8899/gwPuWHvaNr/
  访问口令  f81120ac328d11c11b

   1) 启动          2) 停止
   3) 重启          4) 查看日志
   5) 隧道列表      6) 连接信息
   7) 改端口        8) 改口令
   9) 改访问路径   10) 开机自启开关
  11) 更新         12) 卸载
```

也可以直接带参数用：

```bash
f info       # 连接信息
f list       # 隧道列表
f restart    # 重启
f log        # 跟踪日志
f update     # 更新到最新版
f uninstall  # 卸载
```

隧道状态存在 `/var/lib/fanout/state.json`，重启后自动恢复，端口保持不变。

健康检查每 10 秒跑一次，比对出口 IP 是否还是建立隧道时那个——openvpn 挂掉后
netns 仍能经母机 NAT 出网，只看通不通会漏判。连续两次不符就自动换节点重连，
槽位和端口不变，原先指向它的分流规则会自动改指新节点；重连期间这些规则暂不生效，命中的流量按原路由走。

用真实的 sing-box 校验生成的配置：`FANOUT_SINGBOX_BIN=/etc/sing-box/sing-box go test -run RealSingBox .`

## 已知限制

- SOCKS5 支持 CONNECT 和 UDP ASSOCIATE，DNS/QUIC 这类 UDP 也走隧道
  （感谢 [@zsawi](https://github.com/zsawi) 的 [#22](https://github.com/byJoey/fanout/pull/22)）。
  域名仍在本机解析。
- VPN Gate 是志愿者节点，有相当比例已下线或满员（`AUTH_FAILED`）。
  启动时连不上会自动顺着同地区候选往下试，最多 6 个。
- fanout 停掉时 sing-box（服务模式）仍在跑，配置里的分流规则还指着已经没了的 SOCKS5 端口，
  命中规则的流量会不通，直到 fanout 重新起来恢复隧道。没命中规则的流量不受影响。
- 出口没连通时规则会被暂时略过，命中的流量按原路由（通常是直连）出去，而不是被拦下；
  如果你分流是为了"绝不能从母机 IP 出去"，要留意这段窗口。
- 暂不支持按用户（`auth_user`）、端口、协议等其它条件分流，只有入站 + 域名 / IP / 规则集。
- 管理界面只有随机路径 + 口令登录，没有 HTTPS。放公网建议前面套一层反代。
- 换节点会避开这条出口之前用过的节点（最多记 16 个），同地区都换过一轮后
  从头开始。一个地区只有一个可用节点时换不动，界面会直接说明原因。

## 许可

[MIT](LICENSE)。

节点来自 [VPN Gate](https://www.vpngate.net/)（筑波大学的学术实验项目），
本工具只是调用其公开的节点列表并用官方 openvpn 客户端连接，不修改也不代理其服务。
使用时请遵守 VPN Gate 的条款和你所在地的法律。

## 交流

- 交流群：<https://t.me/+ft-zI76oovgwNmRh>
- 视频教程：<https://youtube.com/@joeyblog>
- 博客：<https://joeyblog.net>

用着有问题、或者想要什么功能，去群里说或提 issue。
