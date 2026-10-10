package main

// 用户限速 / 流量限制（与 sb.sh 共用 /etc/sing-box/user_manager 里的数据）。
//
//   - 限速：speed_limits.json 按用户名存，speed_limit_sync.py 生成 00-limiter.json
//     （bandwidth-limiter 出站，内部 route 复制全部分流规则，所以限速和分流同时生效）。
//     需要带 bandwidth-limiter 的 sing-box 内核。
//   - 流量限制：limits/<用户名>.json，由 sb.sh 装的 singbox-traffic 服务统计、到量停用。
//
// 两边读写同一份文件，网页上改了 sb.sh 里立刻能看到，反之亦然。

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed speed_limit_sync.py
var speedSyncPy []byte

const (
	umDataDir        = "/etc/sing-box/user_manager"
	umSpeedStore     = umDataDir + "/speed_limits.json"
	umSpeedSyncPy    = umDataDir + "/speed_limit_sync.py"
	umLimitDir       = umDataDir + "/limits"
	umTrafficDir     = umDataDir + "/traffic"
	umTrafficState   = umTrafficDir + "/state.json"
	umTrafficPy      = umTrafficDir + "/singbox_traffic.py"
	umResetDir       = umTrafficDir + "/reset_requests"
	umDisabledDir    = umDataDir + "/disabled_users"
	umSpeedDropIn    = "/etc/systemd/system/sing-box.service.d/10-speed-limit.conf"
	umTrafficService = "singbox-traffic"
)

var umMu sync.Mutex

// UserSpeed 是一个用户的限速。
type UserSpeed struct {
	Speed string `json:"speed"` // 原样存的值，如 10MB、500KB、100Mbps
	Mode  string `json:"mode"`  // bidirectional / upload / download
	Bps   int64  `json:"bps"`   // 换算成 字节/秒
	Text  string `json:"text"`  // 1 MB/s（实际网速 8 Mbps）
}

// UserTrafficLimit 是一个用户的流量限制。
type UserTrafficLimit struct {
	Enabled         bool    `json:"enabled"`
	LimitBytes      int64   `json:"limit_bytes"`
	LimitValue      float64 `json:"limit_value"`
	LimitUnit       string  `json:"limit_unit"`
	Period          string  `json:"period"`
	DisabledByLimit bool    `json:"disabled_by_limit"`
}

// UserView 是网页「用户」列表里的一行。
type UserView struct {
	Name         string            `json:"name"`
	Inbounds     []string          `json:"inbounds"`
	Missing      bool              `json:"missing"` // 不在任何入站里（比如到量被停用）
	Uplink       int64             `json:"uplink"`
	Downlink     int64             `json:"downlink"`
	Total        int64             `json:"total"`
	PeriodTotal  int64             `json:"period_total"`
	Speed        *UserSpeed        `json:"speed,omitempty"`
	Traffic      *UserTrafficLimit `json:"traffic,omitempty"`
	TrafficRules int               `json:"rules"`
}

// UsersView 是 /api/users 的返回。
type UsersView struct {
	Users []UserView `json:"users"`
	// TrafficReady 表示装了 sb.sh 的流量统计服务，流量限制才能用
	TrafficReady bool `json:"traffic_ready"`
	// SpeedReady 表示 sing-box 内核带 bandwidth-limiter
	SpeedReady bool `json:"speed_ready"`
}

// ---- 换算 ----

var speedRe = regexp.MustCompile(`^(\d+)([A-Za-z]*)$`)

// speedToBps 按 sing-box 的规则换算：MB = 1000*1000 字节/秒，Mbps = MB/8。
func speedToBps(s string) int64 {
	m := speedRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0
	}
	n, _ := strconv.ParseFloat(m[1], 64)
	switch m[2] {
	case "Kbps":
		return int64(n * 1000 / 8)
	case "Mbps":
		return int64(n * 1000000 / 8)
	case "Gbps":
		return int64(n * 1000000000 / 8)
	}
	switch strings.ToLower(m[2]) {
	case "k", "kb", "kbps":
		return int64(n * 1000)
	case "m", "mb", "mbps":
		return int64(n * 1000000)
	case "g", "gb", "gbps":
		return int64(n * 1000000000)
	}
	return int64(n)
}

func trimNum(v float64) string {
	s := strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64)
	return s
}

// speedText: 1 MB/s（实际网速 8 Mbps）
func speedText(bps int64) string {
	var a string
	switch {
	case bps >= 1000000:
		a = trimNum(float64(bps)/1000000) + " MB/s"
	case bps >= 1000:
		a = trimNum(float64(bps)/1000) + " KB/s"
	default:
		a = strconv.FormatInt(bps, 10) + " B/s"
	}
	mbps := float64(bps) * 8 / 1000000
	var b string
	if mbps >= 1 {
		b = trimNum(mbps) + " Mbps"
	} else {
		b = trimNum(float64(bps)*8/1000) + " Kbps"
	}
	return a + "（实际网速 " + b + "）"
}

// parseSpeedInput 把网页输入转成存盘格式：10 → 10MB，0.5 → 500KB，100Mbps / 500KB 原样。
func parseSpeedInput(in string) (string, error) {
	s := strings.ReplaceAll(strings.TrimSpace(in), " ", "")
	if s == "" {
		return "", fmt.Errorf("请输入限速")
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		if v <= 0 {
			return "", fmt.Errorf("限速必须大于 0")
		}
		if v == math.Trunc(v) {
			return strconv.FormatInt(int64(v), 10) + "MB", nil
		}
		kb := int64(v * 1000)
		if kb <= 0 {
			return "", fmt.Errorf("限速太小")
		}
		return strconv.FormatInt(kb, 10) + "KB", nil
	}
	m := regexp.MustCompile(`^(\d+)([A-Za-z]+)(/[sS])?$`).FindStringSubmatch(s)
	if m == nil || m[1] == "0" || strings.Trim(m[1], "0") == "" {
		return "", fmt.Errorf("格式不对，例如 10、0.5、500KB、100Mbps")
	}
	switch strings.ToLower(m[2]) {
	case "kbps":
		return m[1] + "Kbps", nil
	case "mbps":
		return m[1] + "Mbps", nil
	case "gbps":
		return m[1] + "Gbps", nil
	case "k", "kb":
		return m[1] + "KB", nil
	case "m", "mb":
		return m[1] + "MB", nil
	case "g", "gb":
		return m[1] + "GB", nil
	}
	return "", fmt.Errorf("不认识的单位 %s", m[2])
}

// ---- 读数据 ----

func readJSONFile(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func writeJSONFileAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func loadSpeedStore() map[string]map[string]any {
	var raw struct {
		Users map[string]map[string]any `json:"users"`
	}
	_ = readJSONFile(umSpeedStore, &raw)
	if raw.Users == nil {
		raw.Users = map[string]map[string]any{}
	}
	return raw.Users
}

// limitFiles 返回 用户名 -> 限制文件路径（文件里的 user 字段为准）。
func limitFiles() map[string]string {
	out := map[string]string{}
	entries, _ := os.ReadDir(umLimitDir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(umLimitDir, e.Name())
		var d struct {
			User string `json:"user"`
		}
		if readJSONFile(p, &d) == nil && d.User != "" {
			out[d.User] = p
		}
	}
	return out
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	}
	return 0
}

func trafficReady() bool {
	if _, err := os.Stat(umTrafficPy); err != nil {
		return false
	}
	if hasCmd("systemctl") {
		return exec.Command("systemctl", "cat", umTrafficService+".service").Run() == nil
	}
	return true
}

var speedReadyCache struct {
	sync.Mutex
	key string
	val bool
}

// speedReady 看 sing-box 内核里有没有 bandwidth-limiter：分块扫描二进制里的类型名
// （不整个读进内存，小内存机器上几十 MB 的二进制会把进程撑爆），按文件大小和修改时间缓存。
func speedReady() bool {
	fi, err := os.Stat(singbox.Bin)
	if err != nil {
		return false
	}
	key := fmt.Sprintf("%d-%d", fi.Size(), fi.ModTime().UnixNano())
	speedReadyCache.Lock()
	defer speedReadyCache.Unlock()
	if speedReadyCache.key == key {
		return speedReadyCache.val
	}
	ok := fileContains(singbox.Bin, []byte("bandwidth-limiter"))
	speedReadyCache.key, speedReadyCache.val = key, ok
	return ok
}

func fileContains(path string, needle []byte) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 256*1024+len(needle))
	keep := 0
	for {
		n, err := f.Read(buf[keep:])
		if n > 0 {
			chunk := buf[:keep+n]
			if bytes.Contains(chunk, needle) {
				return true
			}
			keep = len(needle) - 1
			if keep > len(chunk) {
				keep = len(chunk)
			}
			copy(buf, chunk[len(chunk)-keep:])
		}
		if err != nil {
			return false
		}
	}
}

// countUserRules 统计各用户名在配置目录里（不含 00-limiter.json）的分流规则条数。
func countUserRules() map[string]int {
	out := map[string]int{}
	files, _ := filepath.Glob(filepath.Join(singbox.ConfDir, "*.json"))
	for _, fn := range files {
		if filepath.Base(fn) == limiterFile {
			continue
		}
		var doc struct {
			Route struct {
				Rules []map[string]any `json:"rules"`
			} `json:"route"`
		}
		if readJSONFile(fn, &doc) != nil {
			continue
		}
		for _, r := range doc.Route.Rules {
			act := str(r["action"])
			if act != "" && act != "route" {
				continue
			}
			for _, u := range strList(r["auth_user"]) {
				out[u]++
			}
		}
	}
	return out
}

func listUsers() UsersView {
	umMu.Lock()
	defer umMu.Unlock()
	view := UsersView{Users: []UserView{}, TrafficReady: trafficReady(), SpeedReady: speedReady()}

	where := map[string][]string{}
	var order []string
	for _, a := range scanAdopted(singbox.ConfDir) {
		for _, u := range a.Users {
			if _, ok := where[u]; !ok {
				order = append(order, u)
			}
			where[u] = append(where[u], a.Tag)
		}
	}
	limits := limitFiles()
	speeds := loadSpeedStore()
	// 到量被停用的用户已经不在入站里，也要列出来，才能解除
	var extra []string
	for u := range limits {
		if _, ok := where[u]; !ok {
			extra = append(extra, u)
		}
	}
	sort.Strings(extra)

	var state struct {
		Users map[string]map[string]any `json:"users"`
	}
	_ = readJSONFile(umTrafficState, &state)
	rules := countUserRules()

	for _, name := range append(order, extra...) {
		v := UserView{Name: name, Inbounds: where[name], TrafficRules: rules[name]}
		if v.Inbounds == nil {
			v.Inbounds = []string{}
			v.Missing = true
		}
		if s := state.Users[name]; s != nil {
			v.Uplink, v.Downlink = toInt64(s["uplink"]), toInt64(s["downlink"])
			v.Total = toInt64(s["total"])
			if v.Total == 0 {
				v.Total = v.Uplink + v.Downlink
			}
			v.PeriodTotal = toInt64(s["period_total"])
		}
		if sp := speeds[name]; sp != nil {
			speed, mode := str(sp["speed"]), str(sp["mode"])
			if mode == "" {
				mode = "bidirectional"
			}
			bps := speedToBps(speed)
			v.Speed = &UserSpeed{Speed: speed, Mode: mode, Bps: bps, Text: speedText(bps)}
		}
		if p, ok := limits[name]; ok {
			var t UserTrafficLimit
			if readJSONFile(p, &t) == nil && t.Enabled && t.LimitBytes > 0 {
				if t.Period == "" {
					t.Period = "none"
				}
				v.Traffic = &t
			} else if t.DisabledByLimit {
				v.Traffic = &t
			}
		}
		view.Users = append(view.Users, v)
	}
	return view
}

// ---- 限速 ----

func installSpeedHook() error {
	if err := os.MkdirAll(umDataDir, 0700); err != nil {
		return err
	}
	if cur, err := os.ReadFile(umSpeedSyncPy); err != nil || string(cur) != string(speedSyncPy) {
		if err := os.WriteFile(umSpeedSyncPy, speedSyncPy, 0700); err != nil {
			return err
		}
	}
	if !hasCmd("systemctl") || !dirExists("/run/systemd/system") {
		return nil
	}
	py, err := exec.LookPath("python3")
	if err != nil {
		return fmt.Errorf("没有 python3")
	}
	want := "[Service]\nExecStartPre=-" + py + " " + umSpeedSyncPy + "\nExecReload=\nExecReload=-" + py + " " +
		umSpeedSyncPy + "\nExecReload=/bin/kill -HUP $MAINPID\n"
	if cur, err := os.ReadFile(umSpeedDropIn); err == nil && string(cur) == want {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(umSpeedDropIn), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(umSpeedDropIn, []byte(want), 0644); err != nil {
		return err
	}
	return exec.Command("systemctl", "daemon-reload").Run()
}

func runSpeedSync() error {
	if err := installSpeedHook(); err != nil {
		return err
	}
	out, err := exec.Command("python3", umSpeedSyncPy).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if lines := strings.Split(msg, "\n"); len(lines) > 3 {
			msg = strings.Join(lines[len(lines)-3:], "\n")
		}
		return fmt.Errorf("限速配置检查失败：%s", msg)
	}
	return nil
}

func reloadSingbox() error {
	if hasCmd("systemctl") && dirExists("/run/systemd/system") {
		if err := exec.Command("systemctl", "reload", "sing-box").Run(); err != nil {
			return exec.Command("systemctl", "restart", "sing-box").Run()
		}
		return nil
	}
	if hasCmd("rc-service") {
		if err := exec.Command("rc-service", "sing-box", "reload").Run(); err != nil {
			return exec.Command("rc-service", "sing-box", "restart").Run()
		}
	}
	return nil
}

// setUserSpeed 设置（speed 非空）或取消（speed 为空）一个用户的限速。
func setUserSpeed(name, input, mode string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("用户名为空")
	}
	speed := ""
	if s := strings.TrimSpace(input); s != "" && s != "0" {
		v, err := parseSpeedInput(s)
		if err != nil {
			return err
		}
		speed = v
		if !speedReady() {
			return fmt.Errorf("当前 sing-box 内核不支持限速，请先换成带 bandwidth-limiter 的 -xhttp-limiter 版本")
		}
	}
	switch mode {
	case "upload", "download":
	default:
		mode = "bidirectional"
	}

	umMu.Lock()
	defer umMu.Unlock()
	var raw map[string]any
	if readJSONFile(umSpeedStore, &raw) != nil || raw == nil {
		raw = map[string]any{}
	}
	users, _ := raw["users"].(map[string]any)
	if users == nil {
		users = map[string]any{}
	}
	old, had := users[name]
	if speed == "" {
		if !had {
			return nil
		}
		delete(users, name)
	} else {
		users[name] = map[string]any{"speed": speed, "mode": mode}
	}
	raw["users"] = users
	if err := writeJSONFileAtomic(umSpeedStore, raw); err != nil {
		return err
	}
	if err := runSpeedSync(); err != nil {
		if had {
			users[name] = old
		} else {
			delete(users, name)
		}
		raw["users"] = users
		_ = writeJSONFileAtomic(umSpeedStore, raw)
		_ = runSpeedSync()
		return err
	}
	return reloadSingbox()
}

// ---- 流量限制（与 sing-box-name.sh 的 set_limit / disable_limit / set_limit_period 一致）----

var trafficInputRe = regexp.MustCompile(`^([0-9]+(\.[0-9]+)?)(MB|GB)?$`)

func userInInbounds(name string) bool {
	for _, a := range scanAdopted(singbox.ConfDir) {
		if strIn(a.Users, name) {
			return true
		}
	}
	return false
}

func requestPeriodReset(name string) error {
	if err := os.MkdirAll(umResetDir, 0700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(umResetDir, name), nil, 0600)
}

func restoreUser(name string) error {
	out, err := exec.Command("python3", umTrafficPy, "restore_user", name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("恢复用户失败：%s", strings.TrimSpace(string(out)))
	}
	return nil
}

func periodWindow(period string, now time.Time) (any, any) {
	switch period {
	case "day":
		s := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		return s.Format(time.RFC3339), s.AddDate(0, 0, 1).Format(time.RFC3339)
	case "month":
		s := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		return s.Format(time.RFC3339), s.AddDate(0, 1, 0).Format(time.RFC3339)
	}
	return nil, nil
}

// setUserTraffic 设置流量限制。input: "2" = 2GB，"500MB"，"0" 或空 = 关闭；period: day / month / none。
func setUserTraffic(name, input, period string) error {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("用户名无效")
	}
	if !trafficReady() {
		return fmt.Errorf("没有检测到流量统计服务 %s，流量限制需要先用 sb.sh 安装", umTrafficService)
	}
	switch period {
	case "day", "month", "none":
	default:
		period = ""
	}
	s := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(input), " ", ""))

	umMu.Lock()
	defer umMu.Unlock()
	lf := filepath.Join(umLimitDir, name+".json")
	if p, ok := limitFiles()[name]; ok {
		lf = p
	}
	data := map[string]any{}
	_ = readJSONFile(lf, &data)

	if s == "" || s == "0" {
		if len(data) == 0 {
			return requestPeriodReset(name)
		}
		wasDisabled, _ := data["disabled_by_limit"].(bool)
		data["user"] = name
		data["enabled"] = false
		data["limit_value"] = 0
		data["limit_unit"] = "GB"
		data["limit_bytes"] = 0
		data["disabled_by_limit"] = false
		if err := writeJSONFileAtomic(lf, data); err != nil {
			return err
		}
		_ = requestPeriodReset(name)
		if wasDisabled {
			return restoreUser(name)
		}
		return nil
	}

	m := trafficInputRe.FindStringSubmatch(s)
	if m == nil {
		return fmt.Errorf("格式不对，例如 2（=2GB）、100MB、1GB")
	}
	num, _ := strconv.ParseFloat(m[1], 64)
	if num <= 0 {
		return fmt.Errorf("限制必须大于 0")
	}
	unit := m[3]
	if unit == "" {
		unit = "GB"
	}
	limitBytes := int64(num * 1024 * 1024)
	if unit == "GB" {
		limitBytes = int64(num * 1024 * 1024 * 1024)
	}
	oldPeriod := str(data["period"])
	if oldPeriod == "" {
		oldPeriod = "none"
	}
	if period == "" {
		period = oldPeriod
	}
	start, end := data["period_start"], data["period_end"]
	if period != oldPeriod || period == "none" {
		start, end = periodWindow(period, time.Now())
	}
	out := map[string]any{
		"user":              name,
		"limit_value":       num,
		"limit_unit":        unit,
		"limit_bytes":       limitBytes,
		"period":            period,
		"period_start":      start,
		"period_end":        end,
		"enabled":           true,
		"disabled_by_limit": false,
		"saved_user":        data["saved_user"],
		"config_file":       data["config_file"],
	}
	if err := writeJSONFileAtomic(lf, out); err != nil {
		return err
	}
	// 本周期流量清零交给 singbox-traffic 服务处理，避免和它同时写 state.json
	if err := requestPeriodReset(name); err != nil {
		return err
	}
	if !userInInbounds(name) {
		if _, err := os.Stat(filepath.Join(umDisabledDir, name)); err == nil {
			return restoreUser(name)
		}
	}
	return nil
}

// ---- HTTP ----

func apiUsers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, listUsers())
}

func apiUserSpeed(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	var in struct {
		Name  string `json:"name"`
		Speed string `json:"speed"`
		Mode  string `json:"mode"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式错误"})
		return
	}
	if err := setUserSpeed(in.Name, in.Speed, in.Mode); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func apiUserTraffic(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	var in struct {
		Name   string `json:"name"`
		Limit  string `json:"limit"`
		Period string `json:"period"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式错误"})
		return
	}
	if err := setUserTraffic(in.Name, in.Limit, in.Period); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
