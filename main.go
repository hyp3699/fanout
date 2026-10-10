package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// version 由构建时通过 -ldflags 注入。
var version = "dev"

func main() {
	var (
		webPort  = flag.Int("web", 8899, "Web 管理端口")
		maxSlots = flag.Int("max", 20, "最多同时运行的隧道数")
		workDir  = flag.String("dir", "/var/lib/fanout", "工作目录")
	)
	sbBin := flag.String("singbox-bin", "/etc/sing-box/sing-box", "sing-box 可执行文件")
	sbConf := flag.String("singbox-conf", "/etc/sing-box/conf", "sing-box 配置目录（sing-box run -C 加载的那个），fanout 只写其中的 fanout-*.json")
	sbService := flag.String("singbox-service", "sing-box", "sing-box 的 systemd/OpenRC 服务名；存在时用它 reload，填 none 则由 fanout 自己托管进程")
	publicIP := flag.String("ip", "", "母机公网 IPv4，用于分享链接/SOCKS5 地址；留空则自动探测")
	showVersion := flag.Bool("version", false, "显示版本后退出")
	flag.Parse()

	if *publicIP == "" {
		*publicIP = os.Getenv("FANOUT_PUBLIC_IP")
	}

	if *showVersion {
		fmt.Println("fanout", version)
		return
	}

	if os.Geteuid() != 0 {
		log.Fatal("需要 root 权限（要创建 netns 和改 iptables）")
	}
	if err := os.MkdirAll(*workDir, 0700); err != nil {
		log.Fatalf("创建工作目录失败: %v", err)
	}

	// 先记下母机的网络命名空间，后面所有子进程都从这里起。
	// 必须赶在建任何隧道之前，那之后线程就可能被带进隧道里了
	if err := initMainNetns(); err != nil {
		log.Fatal(err)
	}

	// 同一个工作目录只许跑一个实例：两份会共用 state.json 互相覆盖，隧道记录直接丢
	unlock, err := lockWorkDir(*workDir)
	if err != nil {
		log.Fatal(err)
	}
	defer unlock()

	// 定下这台机器上属于本实例的 netns 名与网段。默认目录沿用老名字，
	// 换了目录就自动隔离，免得两个实例互相拆隧道（见 instance.go）
	if err := initInstance(*workDir); err != nil {
		log.Fatalf("初始化实例标识失败: %v", err)
	}
	if instTag != "" {
		log.Printf("非默认工作目录，本实例用 netns fo%s* 与网段 10.%d.x", instTag, instBase)
	}

	setPublicIPOverride(*publicIP)
	go hostPublicIP() // 预热探测，别让首个请求阻塞
	if err := prepareHost(); err != nil {
		log.Fatal(err)
	}

	configureSingBox(*sbBin, *sbConf, *sbService)
	configurePanel(*workDir)
	if p, err := openPanel(); err != nil {
		log.Printf("sing-box 后端暂不可用（可在 Web 界面查看原因）: %v", err)
	} else {
		log.Printf("sing-box 后端: %s", p.Describe())
	}

	mgr := NewManager(*maxSlots, *workDir)
	log.Printf("正在拉取节点列表...")
	if n, err := mgr.RefreshNodes(); err != nil {
		log.Printf("拉取失败（可在 Web 界面重试）: %v", err)
	} else {
		log.Printf("已获取 %d 个节点", n)
	}

	if n, err := mgr.restoreState(); err != nil {
		log.Printf("恢复上次状态失败: %v", err)
	} else if n > 0 {
		log.Printf("正在恢复上次的 %d 条隧道", n)
	}

	go mgr.WatchHealth()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		log.Println("正在清理所有隧道...")
		mgr.Shutdown()
		closePanel()
		unlock() // os.Exit 会绕过 defer，这里手动放锁
		os.Exit(0)
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex)
	mux.HandleFunc("/api/nodes", apiNodes(mgr))
	mux.HandleFunc("/api/tunnels", apiTunnels(mgr))
	mux.HandleFunc("/api/start", apiStart(mgr))
	mux.HandleFunc("/api/stop", apiStop(mgr))
	mux.HandleFunc("/api/swap", apiSwap(mgr))
	mux.HandleFunc("/api/cred", apiCred(mgr))
	mux.HandleFunc("/api/refresh", apiRefresh(mgr))
	mux.HandleFunc("/api/regions", apiRegions(mgr))
	mux.HandleFunc("/api/provision", apiProvision(mgr))
	mux.HandleFunc("/api/jobs", apiJobs(mgr))
	mux.HandleFunc("/api/jobs/dismiss", apiJobDismiss(mgr))
	mux.HandleFunc("/api/exits", apiExits(mgr))
	mux.HandleFunc("/api/backend", apiBackendStatus)
	// 入站只读：fanout 不建、不改、不删入站，只列出配置目录里已有的
	mux.HandleFunc("/api/inbounds", apiInbounds)
	mux.HandleFunc("/api/inbounds/detail", apiInboundDetail)
	mux.HandleFunc("/api/inbounds/links", apiInboundLinks)
	// 分流规则
	mux.HandleFunc("/api/rules", apiRules(mgr))
	mux.HandleFunc("/api/rules/save", apiRuleSave(mgr))
	mux.HandleFunc("/api/rules/delete", apiRuleDelete(mgr))
	mux.HandleFunc("/api/rules/move", apiRuleMove(mgr))
	mux.HandleFunc("/api/rules/enable", apiRuleEnable(mgr))
	// 用户限速 / 流量限制（与 sb.sh 共用数据）
	mux.HandleFunc("/api/users", apiUsers)
	mux.HandleFunc("/api/users/speed", apiUserSpeed)
	mux.HandleFunc("/api/users/traffic", apiUserTraffic)

	auth, created, err := NewAuth(*workDir)
	if err != nil {
		log.Fatalf("初始化访问口令失败: %v", err)
	}
	if created {
		log.Printf("已生成访问口令，见 %s", filepath.Join(*workDir, "password"))
	}

	bpCreated, err := initBasePath(*workDir)
	if err != nil {
		log.Fatalf("初始化访问路径失败: %v", err)
	}
	if bpCreated {
		log.Printf("已生成访问路径，见 %s", filepath.Join(*workDir, "basepath"))
	}

	// 用户显式给了 -web 就以命令行为准，否则沿用界面上存过的端口
	portExplicit := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "web" {
			portExplicit = true
		}
	})
	webCfg, err := loadWebSettings(*workDir, *webPort, portExplicit)
	if err != nil {
		log.Fatalf("加载 Web 设置失败: %v", err)
	}

	srv := newWebServer(StripBasePath(auth.Wrap(mux)))
	// 设置面板：改密码 / 改路径 / 改端口 / 改本地监听。
	mux.HandleFunc("/api/settings", apiSettings(auth, srv))
	mux.HandleFunc("/api/update/check", apiUpdateCheck)
	mux.HandleFunc("/api/update/apply", apiUpdateApply)

	log.Printf("管理界面: http://<本机IP>%s%s/", webCfg.listenAddrString(), currentBasePath())
	log.Printf("SOCKS5 端口在 %d-%d 之间随机分配", randPortMin, randPortMax)
	if err := srv.serve(); err != nil {
		log.Fatal(err)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func apiNodes(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nodes, fetched := m.Nodes()
		total := len(nodes)
		// 默认跟挑节点的口径一致：开了"只用家宽"就不列机房节点。
		// 带 all=1 能看到完整列表，用来确认过滤掉了多少。
		if residentialOnly() && r.URL.Query().Get("all") != "1" {
			kept := make([]Node, 0, len(nodes))
			for _, n := range nodes {
				if n.Residential {
					kept = append(kept, n)
				}
			}
			nodes = kept
		}
		shown := len(nodes)
		if len(nodes) > 200 {
			nodes = nodes[:200]
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"nodes":            nodes,
			"fetched":          fetched,
			"total":            total,
			"available":        shown,
			"residential_only": residentialOnly(),
		})
	}
}

func apiTunnels(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, m.Tunnels())
	}
}

func apiStart(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host := r.URL.Query().Get("host")
		if host == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少 host 参数"})
			return
		}
		nodes, _ := m.Nodes()
		for _, n := range nodes {
			if n.HostName == host {
				t, err := m.Start(n)
				if err != nil {
					writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
					return
				}
				writeJSON(w, http.StatusOK, t)
				return
			}
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "节点不存在，可能列表已过期"})
	}
}

func apiStop(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slot, err := strconv.Atoi(r.URL.Query().Get("slot"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slot 参数无效"})
			return
		}
		if err := m.Stop(slot); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "已停止"})
	}
}

func apiRefresh(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, err := m.RefreshNodes()
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"count": n})
	}
}

// apiSwap 就地把一个出口换到别的节点，端口不变。
func apiSwap(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slot, err := strconv.Atoi(r.URL.Query().Get("slot"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slot 参数无效"})
			return
		}
		if err := m.Swap(slot); err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "正在换节点"})
	}
}

// apiRegions 给新建向导用：各地区还剩多少空闲节点。
func apiRegions(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, m.Regions())
	}
}

// apiCred 改一个出口的 SOCKS5 用户名口令。两个参数都留空表示随机重置。
func apiCred(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		slot, err := strconv.Atoi(q.Get("slot"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slot 参数无效"})
			return
		}
		cred, err := m.SetCred(slot, SocksCred{
			User: strings.TrimSpace(q.Get("user")),
			Pass: strings.TrimSpace(q.Get("pass")),
		})
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"user": cred.User,
			"pass": cred.Pass,
		})
	}
}

// apiSettings 管理界面自身的设置：改密码 / 改路径 / 改端口 / 改本地监听。
// GET 返回当前值（不含明文口令）；POST 按传入的字段逐项应用，任一项失败即整体回报。
func apiSettings(auth *Auth, srv *webServer) http.HandlerFunc {
	type settingsReq struct {
		Password        *string `json:"password"`         // 非空则改口令
		BasePath        *string `json:"base_path"`        // 提供即改访问路径（空串=去掉前缀）
		Port            *int    `json:"port"`             // 提供即改监听端口
		ListenAddr      *string `json:"listen_addr"`      // 提供即改监听地址
		ResidentialOnly *bool   `json:"residential_only"` // 提供即改"只用家宽"
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var in settingsReq
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式错误"})
				return
			}

			// 改口令
			if in.Password != nil && *in.Password != "" {
				if err := auth.SetPassword(*in.Password); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
					return
				}
			}
			// 改访问路径
			if in.BasePath != nil {
				if _, err := setBasePath(*in.BasePath); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
					return
				}
			}
			// 改"只用家宽"。放在改端口之前：applyWebSettings 会整份覆盖设置，
			// 顺序颠倒会把这个开关写回旧值。
			if in.ResidentialOnly != nil {
				if err := setResidentialOnly(*in.ResidentialOnly); err != nil {
					writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
					return
				}
			}
			// 改端口 / 监听地址：合成一份新的 WebSettings 一起应用，避免绑两次
			if in.Port != nil || in.ListenAddr != nil {
				next := getWebSettings()
				if in.Port != nil {
					next.Port = *in.Port
				}
				if in.ListenAddr != nil {
					next.ListenAddr = *in.ListenAddr
				}
				if err := srv.applyWebSettings(next); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
					return
				}
			}
		}

		cfg := getWebSettings()
		listen := cfg.ListenAddr
		if listen == "" {
			listen = "0.0.0.0"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"base_path":        currentBasePath(),
			"port":             cfg.Port,
			"listen_addr":      listen,
			"has_password":     true,
			"residential_only": cfg.residentialOnly(),
			"version":          version,
		})
	}
}

// apiUpdateCheck 问 GitHub 最新 release，回报当前/最新版本与更新内容。
func apiUpdateCheck(w http.ResponseWriter, r *http.Request) {
	st, err := checkUpdate()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "检查更新失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// apiUpdateApply 下载最新版替换二进制并重启服务。成功后进程会被拉起成新版本。
func apiUpdateApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "用 POST"})
		return
	}
	st, err := checkUpdate()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "检查更新失败: " + err.Error()})
		return
	}
	if !st.HasUpdate {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restarting": false, "message": "已经是最新版"})
		return
	}
	if err := applyUpdate(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// 先把响应发回去，restartSelf 已排在延迟后触发
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restarting": true, "latest": st.Latest})
}

// apiExits 返回主界面需要的一切：出口、现有入站和分流规则。
func apiExits(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, m.ExitsOf())
	}
}

// apiProvision 接收"开 N 个某地区的出口"这个意图，返回作业 id 供轮询。
// 只开出口，不建入站。
func apiProvision(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		count, err := strconv.Atoi(q.Get("count"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "count 参数无效"})
			return
		}
		job, err := m.Provision(ProvisionRequest{
			Region: q.Get("region"), Count: count,
			EveryRegion: q.Get("every") == "1",
		})
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"job": job.ID()})
	}
}

func apiJobs(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, m.jobs.Views())
	}
}

func apiJobDismiss(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m.jobs.Dismiss(r.URL.Query().Get("id"))
		writeJSON(w, http.StatusOK, map[string]string{"ok": "已关闭"})
	}
}

// apiBackendStatus 报告 sing-box 后端的状态。
func apiBackendStatus(w http.ResponseWriter, r *http.Request) {
	p, err := openPanel()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"available": false,
			"reason":    err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"available": true,
		"kind":      p.Kind(),
		"describe":  p.Describe(),
		"conf_dir":  singbox.ConfDir,
	})
}

// apiInbounds 列出配置目录里现有的入站（只读）。
func apiInbounds(w http.ResponseWriter, r *http.Request) {
	list, err := cachedInbounds()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// apiInboundDetail 返回某个入站的详情，含客户端与可直接复制的分享链接。
func apiInboundDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.URL.Query().Get("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id 参数无效"})
		return
	}
	x, err := openPanel()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	host := r.URL.Query().Get("host")
	if host == "" {
		host = publicHost(r)
	}
	detail, err := x.InboundDetail(id, host)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// publicHost 决定分享链接里的连接地址。母机公网 IPv4 才是客户端真正能连上
// 的地址，所以优先用它；探测不到（比如纯内网）再退回访问 fanout 时用的主机名。
func publicHost(r *http.Request) string {
	if ip := hostPublicIP(); ip != "" {
		return ip
	}
	host := r.Host
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	if host == "" || host == "127.0.0.1" || host == "localhost" {
		return "<服务器IP>"
	}
	return host
}

// apiInboundLinks 批量导出多个入站的分享链接。
func apiInboundLinks(w http.ResponseWriter, r *http.Request) {
	x, err := openPanel()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	var ids []int
	if raw := r.URL.Query().Get("ids"); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(part)); err == nil {
				ids = append(ids, n)
			}
		}
	} else {
		list, err := x.Inbounds()
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		for _, ib := range list {
			ids = append(ids, ib.ID)
		}
	}
	if len(ids) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "没有可导出的入站"})
		return
	}

	host := r.URL.Query().Get("host")
	if host == "" {
		host = publicHost(r)
	}
	links, err := x.InboundLinks(ids, host)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "links": links})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": links})
}

// ---- 分流规则 ----

func requirePost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "用 POST"})
		return false
	}
	return true
}

// apiRules 列出有序的分流规则，带出口状态与是否生效。
func apiRules(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v := m.ExitsOf()
		if v.Panel != "" && v.Backend == "" {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": v.Panel})
			return
		}
		writeJSON(w, http.StatusOK, v.Rules)
	}
}

// apiRuleSave 新建（id 为 0 或不传）或修改一条分流规则。请求体是 JSON：
//
//	{"id":0,"name":"奈飞","enabled":true,"users":["anytls-user1"],"exit":"<节点主机名>",
//	 "domains":"netflix.com\nfull:www.example.com","rule_sets":[{"source":"geosite:netflix"}],
//	 "local_rule_sets":["geosite-openai"],"all":false,"resolve_ip":false}
//
// exit（fanout 出口的节点主机名）和 outbound（配置目录里已有的出站 / 端点 tag，如 direct）二选一；
// local_rule_sets 是配置目录里已有的 route.rule_set tag，直接引用。
func apiRuleSave(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requirePost(w, r) {
			return
		}
		var in RuleInput
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式错误: " + err.Error()})
			return
		}
		p, err := openPanel()
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		rule, err := p.SaveRule(in, m.Tunnels())
		invalidateInbounds()
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, rule)
	}
}

// ruleAction 把删除 / 移动 / 启停的公共部分收拢：校验 POST、解析 id 再调后端。
func ruleAction(m *Manager, what string, do func(p Panel, id int, r *http.Request, tunnels []*Tunnel) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requirePost(w, r) {
			return
		}
		id, err := strconv.Atoi(r.URL.Query().Get("id"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id 参数无效"})
			return
		}
		p, err := openPanel()
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		err = do(p, id, r, m.Tunnels())
		invalidateInbounds()
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": what})
	}
}

func apiRuleDelete(m *Manager) http.HandlerFunc {
	return ruleAction(m, "已删除", func(p Panel, id int, _ *http.Request, t []*Tunnel) error {
		return p.DeleteRule(id, t)
	})
}

// apiRuleMove 上移（dir=up）或下移（dir=down）一位。规则按顺序匹配，先命中先生效。
func apiRuleMove(m *Manager) http.HandlerFunc {
	return ruleAction(m, "已移动", func(p Panel, id int, r *http.Request, t []*Tunnel) error {
		switch r.URL.Query().Get("dir") {
		case "up":
			return p.MoveRule(id, -1, t)
		case "down":
			return p.MoveRule(id, 1, t)
		}
		return fmt.Errorf("dir 只能是 up 或 down")
	})
}

func apiRuleEnable(m *Manager) http.HandlerFunc {
	return ruleAction(m, "已更新", func(p Panel, id int, r *http.Request, t []*Tunnel) error {
		return p.EnableRule(id, r.URL.Query().Get("on") == "1", t)
	})
}
