(function () {
  "use strict";

  var $ = function (id) { return document.getElementById(id); };
  var t = function (key, vars) { return window.I18N ? window.I18N.t(key, vars) : key; };

  var els = {
    siteName: $("siteName"),
    menuBtn: $("menuBtn"),
    menuPanel: $("menuPanel"),
    langBtn: $("langBtn"),
    langPanel: $("langPanel"),
    langLabel: $("langLabel"),
    themeBtn: $("themeBtn"),
    resultsBtn: $("resultsBtn"),
    settingsBtn: $("settingsBtn"),
    dial: $("dial"),
    arc: $("arc"),
    phase: $("phaseLabel"),
    value: $("dialValue"),
    unit: $("dialUnit"),
    gaugeArc: $("gaugeArc"),
    gaugeTicks: $("gaugeTicks"),
    needleGrad: $("needleGrad"),
    gaugeNeedle: $("gaugeNeedle"),
    metricTop: $("metricTop"),
    mtDown: $("mtDown"),
    mtUp: $("mtUp"),
    mtUpItem: $("mtUpItem"),
    pingRow: $("pingRow"),
    expRow: $("expRow"),
    pgPing: $("pgPing"),
    pgDownLat: $("pgDownLat"),
    pgUpLat: $("pgUpLat"),
    ispName: $("ispName"),
    clientIco: $("clientIco"),
    modeToggle: $("modeToggle"),
    protoBlock: $("protoBlock"),
    asnTag: $("asnTag"),
    ipText: $("ipText"),
    serverItem: $("serverItem"),
    serverName: $("serverName"),
    serverLoc: $("serverLoc"),
    moreBtn: $("moreBtn"),
    resultBar: $("resultBar"),
    speedCard: $("speedCard"),
    cardProgress: $("cardProgress"),
    cardProgressFill: $("cardProgressFill"),
    resultId: $("resultId"),
    copyResult: $("copyResult"),
    openResult: $("openResult"),
    foot: $("footNote"),
    settingsPanel: $("settingsPanel"),
    spIsp: $("spIsp"),
    spIp: $("spIp"),
    spLoc: $("spLoc"),
    spGeo: $("spGeo"),
    spNode: $("spNode"),
    spNodeLoc: $("spNodeLoc"),
    spNodeProto: $("spNodeProto"),
    spChangeNode: $("spChangeNode"),
    spPreview: $("spPreview"),
    modal: $("serverModal"),
    nodeList: $("nodeList"),
    closeModal: $("closeModal"),
    toast: $("toast")
  };

  /* ---------------- 圆环（空闲态） ---------------- */

  var R = 110;
  var CIRC = 2 * Math.PI * R;
  els.arc.style.strokeDasharray = CIRC + " " + CIRC;
  els.arc.style.strokeDashoffset = CIRC;

  /* ---------------- 仪表盘（测速态） ---------------- */

  var G = { cx: 200, cy: 200, r: 147, start: 135, sweep: 270, tickR: 104 };
  var G_LEN = 2 * Math.PI * G.r * (G.sweep / 360);
  els.gaugeArc.style.strokeDasharray = G_LEN + " " + G_LEN;
  els.gaugeArc.style.strokeDashoffset = G_LEN;

  var TICK_VALUES = [0, 5, 10, 50, 100, 250, 500, 750, 1000, 2500, 5000, 7500, 10000];

  var state = {
    settings: {
      siteName: "SpeedTest",
      maxConnections: 6,
      downloadSeconds: 15,
      uploadSeconds: 15,
      gaugeMax: 1000,
      defaultNode6: "",
      allowNodeSelect: true,
      showClientIP: true
    },
    nodes: [],
    node: null,
    base: "",
    ip: "",
    isp: "",
    ispEn: "",
    asn: "",
    asnName: "",
    ipVersion: "",
    // pageFamily 是「打开页面这次连接」用的协议族，family 是用户当前选择的协议。
    // 两者不同时需要按协议另发请求才能拿到访客在另一协议下的地址。
    pageFamily: "",
    family: "v4",
    addrs: { v4: [], v6: [] },
    clients: { v4: null, v6: null },
    probing: { v4: null, v6: null },
    localBase: { v4: "", v6: "" },
    mode: "multi",
    running: false,
    ranking: true,
    picked: false,
    abort: null,
    lastResult: null,
    jumpTimer: 0,
    // 管理员登录状态：控制节点地址等敏感信息是否展示
    isAdmin: false
  };

  var gaugeMax = 1000;
  var spinRaf = 0;

  /* 圆环与仪表盘渐变：多连接 = 青蓝，单一连接 = 橙（对齐 speedtest.net） */
  var RING_COLORS = {
    multi: ["#2de5d1", "#1fa4e9"],
    single: ["#ffd684", "#ff886a"]
  };
  var GAUGE_COLORS = {
    // 原版 speedtest.net：左下深蓝 → 中段蓝 → 顶部/右上亮青（不过分发白）
    multi: ["#1f5dff", "#6ae3f8", "#56cdec"],
    single: ["#ffd684", "#ffab7a", "#ff886a"]
  };
  // 上行阶段整条弧换成上行色，和顶部的上行图标同步
  var UP_GAUGE_COLORS = {
    multi: ["#6d28d9", "#a855f7", "#c77dff"],
    single: ["#5fb51e", "#8fd94a", "#b6e878"]
  };

  function setStops(id, colors) {
    var g = document.getElementById(id);
    if (!g) return;
    var stops = g.getElementsByTagName("stop");
    for (var i = 0; i < stops.length; i++) {
      stops[i].setAttribute("stop-color", colors[Math.min(i, colors.length - 1)]);
    }
  }

  function applyMode() {
    var single = state.mode === "single";
    if (els.dial) els.dial.setAttribute("data-mode", single ? "single" : "multi");
    // 单线程是橙/绿配色，多线程是青/紫，靠根节点属性整体换色
    document.documentElement.setAttribute("data-conn", single ? "single" : "multi");
    setStops("ringGrad", single ? RING_COLORS.single : RING_COLORS.multi);
    setStops("gaugeGrad", single ? GAUGE_COLORS.single : GAUGE_COLORS.multi);
  }

  /* ---------------- 小工具 ---------------- */

  function toast(msg) {
    els.toast.textContent = msg;
    els.toast.className = "toast show";
    clearTimeout(els.toast._t);
    els.toast._t = setTimeout(function () { els.toast.className = "toast"; }, 2400);
  }

  // 速度单位跟随「设置」：Mbps（两位小数）或 Kbps（换算后取整）
  function fmtSpeed(v) {
    if (window.PREFS) return PREFS.speedText(v);
    if (!isFinite(v) || v <= 0) return "0.00";
    return v.toFixed(2);
  }

  function speedUnit() {
    return window.PREFS ? PREFS.speedUnit() : "Mbps";
  }

  // 两点间球面距离（公里）：给节点列表显示「距离」用
  function distKm(a, b) {
    if (!a || !b) return NaN;
    if (!a.lat && !a.lon) return NaN;   // 访客坐标未知
    if (!b.lat && !b.lon) return NaN;   // 节点没配经纬度
    if (!isFinite(a.lat) || !isFinite(b.lat)) return NaN;
    var R = 6371;
    var dLat = (b.lat - a.lat) * Math.PI / 180;
    var dLon = (b.lon - a.lon) * Math.PI / 180;
    var s = Math.sin(dLat / 2) * Math.sin(dLat / 2) +
      Math.cos(a.lat * Math.PI / 180) * Math.cos(b.lat * Math.PI / 180) *
      Math.sin(dLon / 2) * Math.sin(dLon / 2);
    return 2 * R * Math.asin(Math.min(1, Math.sqrt(s)));
  }

  // 访客到某个节点的距离文案（任一端没有坐标时返回空串）
  function nodeDistText(n) {
    if (!window.PREFS || !n) return "";
    var me = clientOf(state.family);
    if (!me) return "";
    var km = distKm(me, n);
    if (!isFinite(km)) return "";
    return PREFS.distText(km);
  }

  function fmtPing(v) {
    if (!isFinite(v) || v <= 0) return "--";
    return v < 10 ? v.toFixed(1) : v.toFixed(0);
  }

  // IPv6 地址太长，按「前 4 段 / 后 4 段」折成两行
  function ipLines(ip) {
    ip = (ip || "").trim();
    if (ip.indexOf(":") < 0) return ip;
    var parts = ip.split(":");
    if (parts.length < 4) return ip;
    var mid = Math.ceil(parts.length / 2);
    return parts.slice(0, mid).join(":") + ":\n" + parts.slice(mid).join(":");
  }

  function hostOf(url) {
    return (url || "").replace(/^https?:\/\//i, "").replace(/\/.*$/, "");
  }

  // 地址属于哪个协议族："v4" / "v6"（不合法时返回空串）
  function ipFamilyOf(ip) {
    ip = (ip || "").trim().replace(/^\[|\]$/g, "");
    if (!ip) return "";
    if (ip.indexOf(":") >= 0) return "v6";
    return /^\d{1,3}(\.\d{1,3}){3}$/.test(ip) ? "v4" : "";
  }

  // 由主机名（IP 字面量）拼出与当前页面同协议、同端口的访问基址
  function originOf(host) {
    host = (host || "").trim().replace(/^\[|\]$/g, "");
    if (!host) return "";
    var proto = location.protocol === "https:" ? "https:" : "http:";
    var port = location.port;
    var h = host.indexOf(":") >= 0 ? "[" + host + "]" : host;
    return proto + "//" + h + (port ? ":" + port : "");
  }

  // 后端下发的探测地址可能是裸 IP，也可能是反代域名；统一成正经 origin
  function originOfEntry(v) {
    v = (v || "").trim().replace(/\/+$/, "");
    return /^https?:\/\//i.test(v) ? v : originOf(v);
  }

  // 带超时的 JSON 请求：跨协议补探访客地址时用
  async function fetchJSON(url, timeout) {
    var ctl = typeof AbortController !== "undefined" ? new AbortController() : null;
    var timer = ctl ? setTimeout(function () { ctl.abort(); }, timeout || 4000) : 0;
    try {
      var res = await fetch(url, {
        cache: "no-store",
        mode: "cors",
        signal: ctl ? ctl.signal : undefined
      });
      if (!res.ok) throw new Error("http " + res.status);
      return await res.json();
    } finally {
      if (timer) clearTimeout(timer);
    }
  }

  // 当前协议下该节点使用的接入地址：IPv6 取 url6，IPv4 取 url
  function nodeURL(node) {
    if (!node) return "";
    return ((state.family === "v6" ? node.url6 : node.url) || "").trim();
  }

  // 节点是否支持某个协议；本机节点两个地址都留空时视为走当前访问地址，双栈都算可用
  function hasFamily(node, fam) {
    if (!node) return false;
    var explicit = fam === "v6" ? (node.url6 || "").trim() : (node.url || "").trim();
    if (explicit) return true;
    return !!node.local && !(node.url || "").trim() && !(node.url6 || "").trim();
  }

  function availableNodes() {
    return state.nodes.filter(function (n) { return hasFamily(n, state.family); });
  }

  function baseOf(node) {
    if (!node) return "";
    var u = nodeURL(node).replace(/\/+$/, "");
    // 本机节点没填地址时跟随当前访问地址；切到另一种协议时，
    // 改用探测到的该协议地址，否则测速流量仍会走打开页面的那个协议。
    if (!u && node.local && state.family !== state.pageFamily) {
      u = state.localBase[state.family] || "";
    }
    if (!u || u === location.origin) return "";
    return u;
  }

  function connections() {
    return state.mode === "single" ? 1 : Math.max(1, state.settings.maxConnections || 4);
  }

  /* 原版 speedtest.net 的仪表刻度：一组固定档位标签在弧上【等角度均布】，
     指针/弧填充在相邻档位之间按数值线性插值（不是对数！）。
     实测原版 max=1000：0/5/10/50/100/250/500/750/1000 均分 270°，误差<2°。 */
  var TICK_LADDERS = [
    { max: 10,    vals: [0, 1, 2, 5, 10] },
    { max: 25,    vals: [0, 1, 5, 10, 25] },
    { max: 50,    vals: [0, 5, 10, 25, 50] },
    { max: 100,   vals: [0, 5, 10, 25, 50, 100] },
    { max: 250,   vals: [0, 5, 10, 50, 100, 250] },
    { max: 500,   vals: [0, 10, 25, 50, 100, 250, 500] },
    { max: 1000,  vals: [0, 5, 10, 50, 100, 250, 500, 750, 1000] },
    { max: 2500,  vals: [0, 50, 100, 250, 500, 1000, 2500] },
    { max: 5000,  vals: [0, 100, 250, 500, 1000, 2500, 5000] },
    { max: 10000, vals: [0, 250, 500, 1000, 2500, 5000, 10000] }
  ];

  var _ladderCache = { max: -1, vals: null };

  function tickLadder(max) {
    if (_ladderCache.max === max && _ladderCache.vals) return _ladderCache.vals;
    var base = null;
    for (var i = 0; i < TICK_LADDERS.length; i++) {
      if (max <= TICK_LADDERS[i].max) { base = TICK_LADDERS[i].vals; break; }
    }
    if (!base) base = TICK_VALUES;
    var vals = base.filter(function (v) { return v <= max; });
    if (!vals.length || vals[vals.length - 1] !== max) vals.push(max);
    _ladderCache = { max: max, vals: vals };
    return vals;
  }

  function speedToProgress(v, max) {
    if (v <= 0) return 0;
    var vals = tickLadder(max);
    var n = vals.length;
    if (n < 2 || v >= vals[n - 1]) return 1;
    for (var i = 0; i < n - 1; i++) {
      if (v <= vals[i + 1]) {
        var f = (v - vals[i]) / (vals[i + 1] - vals[i]);
        return Math.max(0, Math.min(1, (i + f) / (n - 1)));
      }
    }
    return 1;
  }

  function setArc(speed, max) {
    els.arc.style.strokeDashoffset = CIRC * (1 - speedToProgress(speed, max));
  }

  // 刻度文字跟随速度单位：Mbps 直接写数字，Kbps 换算后压成 5k / 250k / 1M 这样的短写法
  function tickLabel(v) {
    if (!window.PREFS || PREFS.speedUnit() !== "Kbps") return String(v);
    var k = v * 1000;
    if (k >= 1000000) {
      var m = k / 1000000;
      return (m % 1 ? m.toFixed(1) : String(m)) + "M";
    }
    if (k >= 1000) return (k / 1000) + "k";
    return String(k);
  }

  // 数字变宽会压到刻度：把与读数重叠的那几个刻度暂时藏起来（数字变窄后自动显示回来）
  var trimToken = 0;

  function trimTicks() {
    var v = els.value.getBoundingClientRect();
    if (!v.width) return;
    var texts = els.gaugeTicks.querySelectorAll("text");
    for (var i = 0; i < texts.length; i++) {
      var t = texts[i].getBoundingClientRect();
      var hit = t.right > v.left - 4 && t.left < v.right + 4 &&
        t.bottom > v.top - 3 && t.top < v.bottom + 3;
      texts[i].style.opacity = hit ? "0" : "";
    }
  }

  function scheduleTrim() {
    if (trimToken) return;
    trimToken = requestAnimationFrame(function () {
      trimToken = 0;
      trimTicks();
    });
  }

  function buildTicks(max) {
    // 刻度档位等角度均布：第 i 档位于弧的 i/(n-1) 处（与原版一致）
    var vals = tickLadder(max);
    var n = vals.length;
    var out = "";
    vals.forEach(function (v, i) {
      var p = n > 1 ? i / (n - 1) : 0;
      var a = ((G.start + p * G.sweep) * Math.PI) / 180;
      var x = G.cx + Math.cos(a) * G.tickR;
      var y = G.cy + Math.sin(a) * G.tickR;
      out += '<text x="' + x.toFixed(1) + '" y="' + y.toFixed(1) + '">' + tickLabel(v) + "</text>";
    });
    els.gaugeTicks.innerHTML = out;
    scheduleTrim();
  }

  function setGauge(speed, max) {
    if (!max) max = 1000;
    var p = speedToProgress(speed, max);
    var deg = G.start + p * G.sweep + 90;
    els.gaugeArc.style.strokeDashoffset = G_LEN * (1 - p);
    els.gaugeNeedle.style.transform = "rotate(" + deg.toFixed(2) + "deg)";
    // 指针的高光是「页面上亮、下方暗」的固定方向（原版如此）：
    // 指针朝上时尖端发亮，朝左下时整体偏暗，所以渐变要反向抵消旋转
    if (els.needleGrad) {
      els.needleGrad.setAttribute("gradientTransform",
        "rotate(" + (-deg).toFixed(2) + " " + G.cx + " " + G.cy + ")");
    }
  }

  /* 表盘量程档位：读数稳定后收敛到一个整齐的档位，之后只往上扩，
     避免量程每来一次采样就重算、刻度跟着跳 */
  var GAUGE_STEPS = [10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000];

  function niceMax(v) {
    for (var i = 0; i < GAUGE_STEPS.length; i++) {
      if (v <= GAUGE_STEPS[i]) return GAUGE_STEPS[i];
    }
    return GAUGE_STEPS[GAUGE_STEPS.length - 1];
  }

  /* 一个阶段的实时显示：最近 10 秒平均速率 → 轻微平滑 → 量程锁定。
     输入本身已经是平均值，指针只会平缓爬升；量程在读数稳定后一次性收敛。 */
  function makeMeter(startMax) {
    var ema = 0;
    // 量程固定 1000 Mbps（原版就是这样，刻度不随速度变化）
    var max = startMax || 1000;
    return {
      max: function () { return max; },
      tick: function (inst) {
        if (!(inst > 0)) return;
        ema = ema > 0 ? ema + (inst - ema) * 0.4 : inst;
        setGauge(ema, max);
        setDial(fmtSpeed(ema), speedUnit(), "", true);
      }
    };
  }

  /* ---------------- 体验评级 ---------------- */

  // 速度类：steps 是 4 个门槛，达到几档就给几点（最低 1 点，最高 5 点）
  function levelBySpeed(v, steps) {
    if (!isFinite(v) || v <= 0) return 0;
    var n = 1;
    for (var i = 0; i < steps.length; i++) if (v >= steps[i]) n = i + 2;
    return Math.min(5, n);
  }

  // 延迟类：越小越好
  function levelByPing(ms) {
    if (!isFinite(ms) || ms <= 0) return 0;
    if (ms <= 15) return 5;
    if (ms <= 30) return 4;
    if (ms <= 60) return 3;
    if (ms <= 100) return 2;
    return 1;
  }

  function buildExpDots() {
    var boxes = document.querySelectorAll("#expRow .exp");
    for (var i = 0; i < boxes.length; i++) {
      var dots = boxes[i].querySelector(".exp-dots");
      if (!dots || dots.children.length) continue;
      for (var j = 0; j < 5; j++) dots.appendChild(document.createElement("i"));
    }
  }

  function renderExp(down, up, ping) {
    var lv = {
      web: levelBySpeed(down, [5, 10, 25, 50]),
      game: levelByPing(ping),
      video: levelBySpeed(down, [5, 10, 25, 50]),
      call: levelBySpeed(up, [1, 2, 5, 10])
    };
    var boxes = document.querySelectorAll("#expRow .exp");
    for (var i = 0; i < boxes.length; i++) {
      var dots = boxes[i].querySelectorAll(".exp-dots i");
      var n = lv[boxes[i].getAttribute("data-exp")] || 0;
      for (var j = 0; j < dots.length; j++) dots[j].className = j < n ? "on" : "";
    }
  }

  function setDial(text, unit, phase, numeric) {
    els.value.textContent = text;
    // Kbps 下数字会到六七位，按长度收字号，别压到刻度上
    var cls = "dial-value" + (numeric ? " num" : "");
    if (numeric && unit === "Kbps") {
      var n = (text || "").length;
      if (n >= 7) cls += " xxlong";
      else if (n === 6) cls += " xlong";
    }
    els.value.className = cls;
    els.unit.textContent = unit || "";
    els.phase.textContent = phase || "";
    scheduleTrim();
  }

  function spin(on) {
    if (on) {
      els.arc.style.transition = "none";
      els.arc.style.strokeDasharray = CIRC * 0.16 + " " + CIRC * 0.84;
      var start = performance.now();
      var frame = function () {
        var t = ((performance.now() - start) / 1500) % 1;
        els.arc.style.strokeDashoffset = -t * CIRC;
        spinRaf = requestAnimationFrame(frame);
      };
      frame();
    } else {
      if (spinRaf) cancelAnimationFrame(spinRaf);
      spinRaf = 0;
      els.arc.style.transition = "stroke-dashoffset .2s linear";
      els.arc.style.strokeDasharray = CIRC + " " + CIRC;
    }
  }

  // 卡片底部进度条：0~100，测速各阶段推进
  function setCardProgress(pct) {
    if (!els.cardProgressFill) return;
    els.cardProgressFill.style.width = Math.max(0, Math.min(100, pct)) + "%";
  }

  function resetDial() {
    els.dial.classList.remove("testing", "gauge", "up");
    if (els.speedCard) els.speedCard.classList.remove("testing", "result");
    setCardProgress(0);
    els.arc.style.strokeDashoffset = CIRC;
    els.metricTop.hidden = true;
    els.pingRow.hidden = true;
    els.expRow.hidden = true;
    if (els.mtUpItem) els.mtUpItem.classList.remove("live");
    renderExp(0, 0, 0);
    els.resultBar.hidden = true;
    setDial("GO", "", "", false);
  }

  function enterGaugeMode() {
    els.dial.classList.add("gauge");
    if (els.speedCard) els.speedCard.classList.add("testing");
    setCardProgress(4);
    els.dial.classList.remove("up");
    if (els.mtUpItem) els.mtUpItem.classList.remove("live");
    buildTicks(gaugeMax);
    setGauge(0, gaugeMax);
    els.metricTop.hidden = false;
    els.mtDown.textContent = "--";
    els.mtUp.textContent = "--";
    els.pingRow.hidden = false;
    els.pgPing.textContent = "--";
    els.pgDownLat.textContent = "--";
    els.pgUpLat.textContent = "--";
    els.expRow.hidden = false;
    renderExp(0, 0, 0);
  }

  /* ---------------- 主题 ---------------- */

  function applyTheme(theme) {
    document.documentElement.setAttribute("data-theme", theme);
    try { localStorage.setItem("st_theme", theme); } catch (e) {}
  }

  function initTheme() {
    var saved = null;
    try { saved = localStorage.getItem("st_theme"); } catch (e) {}
    if (!saved) saved = "dark";
    applyTheme(saved);
  }

  els.themeBtn.addEventListener("click", function () {
    var cur = document.documentElement.getAttribute("data-theme");
    applyTheme(cur === "dark" ? "light" : "dark");
  });

  /* ---------------- 初始化 ---------------- */

  function initI18n() {
    if (!window.I18N) return;
    window.I18N.apply();
    if (els.langLabel) els.langLabel.textContent = t("langName");
    var items = document.querySelectorAll("[data-lang]");
    for (var i = 0; i < items.length; i++) {
      items[i].classList.toggle("on", items[i].getAttribute("data-lang") === window.I18N.lang);
    }
  }

  function boot() {
    initTheme();
    initI18n();
    buildExpDots();
    fetch("/api/info", { cache: "no-store" })
      .then(function (r) { return r.json(); })
      .then(function (data) {
        if (data && data.settings) state.settings = Object.assign(state.settings, data.settings);
        state.isAdmin = !!(data && data.admin);
        state.addrs = (data && data.addrs) || { v4: [], v6: [] };
        state.nodes = (data && data.servers) || [];
        if (!state.nodes.length && data && data.node) {
          state.nodes = [Object.assign({ local: true, url: "", url6: "" }, data.node)];
        }
        state.pageFamily = ipFamilyOf(data.ip) || data.ipVersion || "";
        // 打开页面这次连接的信息先落到它自己的协议上
        if (state.pageFamily) {
          state.clients[state.pageFamily] = {
            ip: data.ip || "",
            isp: data.isp || "",
            ispEn: data.ispEn || "",
            asn: data.asn || "",
            asnName: data.asName || "",
            city: data.city || "",
            region: "",
            lat: typeof data.lat === "number" ? data.lat : 0,
            lon: typeof data.lon === "number" ? data.lon : 0
          };
        }
        applySettings();
        // 协议优先级：网址路径 > 上次手动选择 > 本次连接用的协议
        var saved = null;
        try { saved = localStorage.getItem("st_family"); } catch (e) {}
        var fromPath = pathFamily();
        if (fromPath) state.family = fromPath;
        else if (saved === "v4" || saved === "v6") state.family = saved;
        else if (state.pageFamily) state.family = state.pageFamily;
        renderProto();
        syncPath();
        renderClient();
        renderSettings();
        // 选了另一种协议时，先补探该协议下的访客地址
        probeClient(state.family).then(function () {
          loadPreferredNode();
        });
        loadISP();
      })
      .catch(function () {
        els.ipText.textContent = t("offline");
        els.serverName.textContent = t("noServer");
      });
  }

  // 补充 /api/isp 的结果（本地节点、运营商异步查询得到的信息更全）
  function loadISP() {
    fetch("/api/isp", { cache: "no-store" })
      .then(function (r) { return r.json(); })
      .then(function (d) {
        if (!d || !d.ip) return;
        var fam = ipFamilyOf(d.ip);
        var c = state.clients[fam] || {};
        if (d.isp) c.isp = d.isp;
        if (d.ispEn) c.ispEn = d.ispEn;
        if (d.asn) c.asn = d.asn;
        if (d.asName) c.asnName = d.asName;
        if (d.city) c.city = d.city;
        if (d.region) c.region = d.region;
        if (typeof d.lat === "number") c.lat = d.lat;
        if (typeof d.lon === "number") c.lon = d.lon;
        c.ip = d.ip;
        state.clients[fam] = c;
        if (fam === state.family) renderClient();
        renderSettings();
      })
      .catch(function () {});
  }

  // 后台上传的自定义图标：替换首页 logo 里的 SVG 圆环，并同步 favicon。
  // 服务端已在首屏注入 favicon，这里再补一次 logo，保证 settings 到达后一致。
  function applySiteIcon(url) {
    var logo = document.querySelector(".logo");
    if (!logo) return;
    var img = document.getElementById("logoIcon");
    var svg = logo.querySelector(".logo-mark");
    if (url) {
      if (!img) {
        img = document.createElement("img");
        img.id = "logoIcon";
        img.className = "logo-mark";
        img.alt = "";
        if (svg) svg.parentNode.insertBefore(img, svg);
        else logo.insertBefore(img, logo.firstChild);
      }
      if (img.getAttribute("src") !== url) img.src = url;
      if (svg) svg.style.display = "none";
    } else {
      if (img) img.remove();
      if (svg) svg.style.display = "";
    }
  }

  function applySettings() {
    var s = state.settings;
    var suffix = (s.titleSuffix || "").trim() || t("titleSuffixDefault");
    document.title = t("pageTitle", { site: s.siteName, suffix: suffix });
    els.siteName.textContent = s.siteName;
    applySiteIcon(s.iconUrl || "");
    gaugeMax = 1000;
    els.foot.textContent = s.footerNote || "";
    els.foot.hidden = !s.footerNote;
    renderClient();
    renderServer();
  }

  // 运营商名中英双语：中文界面用「中国电信」，英文界面用 China Telecom
  function ispLabelOf(c) {
    if (!c) return "";
    if (window.I18N && window.I18N.ispName) return window.I18N.ispName(c.isp, c.ispEn);
    return c.isp || c.ispEn || "";
  }

  /* ---------- 网址路径跟着协议走 ----------
   * /<v4>、/<v6>（可带语言前缀）既能被后端解析，刷新/分享也会保持协议。
   * 路径段来自后台配置（window.I18N.routes），带默认值兜底。
   */
  function routes() {
    return (window.I18N && window.I18N.routes) || {};
  }
  function rx(s) { return String(s || "").replace(/[.*+?^${}()|[\]\\]/g, "\\$&"); }

  function pathFamily() {
    var p = (location.pathname || "").replace(/\/+$/, "");
    var r = routes();
    if (new RegExp("/" + rx(r.v4 || "ipv4") + "$", "i").test(p)) return "v4";
    if (new RegExp("/" + rx(r.v6 || "ipv6") + "$", "i").test(p)) return "v6";
    return "";
  }

  function syncPath() {
    var r = routes();
    var seg = state.family === "v6" ? (r.v6 || "ipv6") : (r.v4 || "ipv4");
    var segRe = new RegExp("/(" + rx(r.v4 || "ipv4") + "|" + rx(r.v6 || "ipv6") + ")$", "i");
    var base = (location.pathname || "/").replace(segRe, "").replace(/\/+$/, "");
    var next = base + "/" + seg + (location.search || "");
    if (location.pathname + (location.search || "") === next) return;
    try { history.replaceState(null, "", next); } catch (e) {}
  }

  // 后台开关：是否按实测延迟自动选最低的节点（关闭则按后台节点顺序）
  function autoPick() {
    var s = state.settings || {};
    return s.autoSelectNode !== false;
  }

  // 某个协议下已探测到的访客信息（未探测到返回 null）
  function clientOf(fam) {
    return state.clients[fam] || null;
  }

  // IP 那行右对齐时让出运营商名右边的空隙，让 IP 的结尾正好落在服务商文字的右边缘下方，
  // 两行右边缘不会一高一低地看着别扭（运营商名长短、AS 号位数都会变，所以要实测）
  function layoutClientIP() {
    var box = document.querySelector(".conn-client");
    var name = els.ispName;
    if (!box || !name) return;
    var inset = 0;
    if ((name.textContent || "").trim()) {
      var b = box.getBoundingClientRect();
      var n = name.getBoundingClientRect();
      if (n.width) inset = Math.max(0, Math.round(b.right - n.right));
    }
    box.style.setProperty("--ip-inset", inset + "px");
  }

  function renderClient() {
    var s = state.settings;
    var c = clientOf(state.family);
    if (!c) {
      // 该协议还没探测出地址：不要拿另一个协议的地址冒充
      var waiting = !!state.probing[state.family];
      els.ispName.textContent = waiting ? t("detecting") : t("yourNetwork");
      els.asnTag.hidden = true;
      els.ipText.textContent = waiting ? t("detecting") : t("noAddrForFamily");
      state.ip = "";
      state.isp = state.ispEn = state.asn = state.asnName = "";
      layoutClientIP();
      return;
    }
    els.ispName.textContent = ispLabelOf(c) || (c.ip ? t("unknownIsp") : t("yourNetwork"));
    if (c.asn) {
      els.asnTag.textContent = c.asn;
      els.asnTag.title = c.asnName || "";
      els.asnTag.hidden = false;
    } else {
      els.asnTag.hidden = true;
    }
    els.ipText.textContent = s.showClientIP ? (c.ip ? ipLines(c.ip) : t("noPublicIp")) : t("hidden");
    // 上报与「摘要」用的都是当前协议下的信息
    state.ip = c.ip || "";
    state.ipVersion = ipFamilyOf(c.ip);
    state.isp = c.isp || "";
    state.ispEn = c.ispEn || "";
    state.asn = c.asn || "";
    state.asnName = c.asnName || "";
    layoutClientIP();
  }

  /* ---------- 按协议补探访客地址 ----------
   * 浏览器只会用它自己选定的协议跟我们通信：页面若走 IPv6 打开，
   * 同源请求一律是 IPv6，服务端自然也只看得到 IPv6 地址。
   * 想知道用户选 IPv4 时他的 IPv4 地址，只能另发一个走 IPv4 的请求，
   * 所以这里用「本机在目标协议下的地址」或「该协议节点的地址」当目标，
   * 请求 /api/isp 拿对方的视角地址（同源时直接就是当前连接）。
   */
  function probeOrigins(fam) {
    var out = [];
    var push = function (u) {
      u = (u || "").trim().replace(/\/+$/, "");
      if (u && out.indexOf(u) < 0) out.push(u);
    };
    (state.addrs[fam] || []).forEach(function (ip) { push(originOfEntry(ip)); });
    state.nodes.forEach(function (n) {
      if (!n) return;
      var u = fam === "v6" ? n.url6 : n.url;
      if (!u) return;
      u = u.trim();
      // 裸地址（未带协议）按页面端口补全；带端口的节点原样使用
      if (!/^https?:\/\//i.test(u)) u = originOf(hostOf(u));
      push(u);
    });
    // 候选太多会让「正在获取…」等太久，取前三个
    return out.slice(0, 3);
  }

  // 把 /api/isp 的返回整理成该协议的访客信息
  function toClient(d) {
    return {
      ip: d.ip,
      isp: d.isp || "",
      ispEn: d.ispEn || "",
      asn: d.asn || "",
      asnName: d.asName || "",
      city: d.city || "",
      region: d.region || "",
      lat: typeof d.lat === "number" ? d.lat : 0,
      lon: typeof d.lon === "number" ? d.lon : 0
    };
  }

  function probeClient(fam, force) {
    // 命中缓存也必须重绘：否则切回已探过的协议时，信息行还停在上一个协议的文字上
    if (!force && state.clients[fam]) {
      renderClient();
      return Promise.resolve(state.clients[fam]);
    }
    if (state.probing[fam]) return state.probing[fam];
    var origins = probeOrigins(fam);
    var task = new Promise(function (resolve) {
      if (!origins.length) { resolve(null); return; }
      var settled = false;
      var left = origins.length;
      var finish = function (c) {
        if (settled) return;
        settled = true;
        resolve(c);
      };
      // 几个候选地址同时打，谁先给出该协议的地址就用谁（串行要等 3 个超时）
      origins.forEach(function (origin) {
        fetchJSON(origin + "/api/isp", 3000).then(function (d) {
          if (settled || !d || !d.ip || ipFamilyOf(d.ip) !== fam) return;
          var c = toClient(d);
          state.clients[fam] = c;
          // 这个地址已经验证可达，跨协议测速就用它做基址
          (state.addrs[fam] || []).forEach(function (ip) {
            if (originOfEntry(ip) === origin) state.localBase[fam] = origin;
          });
          finish(c);
        }).catch(function () {}).then(function () {
          if (--left === 0) finish(null);
        });
      });
    }).then(function (c) {
      state.probing[fam] = null;
      renderClient();
      renderSettings();
      return c;
    });
    state.probing[fam] = task;
    renderClient();
    return task;
  }

  /* ---------------- 设置面板 ---------------- */

  function readPrefsIntoUI() {
    var groups = document.querySelectorAll("#settingsPanel .sp-opts");
    for (var i = 0; i < groups.length; i++) {
      var key = groups[i].getAttribute("data-pref");
      var val = window.PREFS ? PREFS.get(key) : "";
      var opts = groups[i].querySelectorAll(".sp-opt");
      for (var j = 0; j < opts.length; j++) {
        opts[j].classList.toggle("active", opts[j].getAttribute("data-val") === val);
      }
    }
    var sel = document.getElementById("spDateFmt");
    if (sel && window.PREFS) sel.value = PREFS.get("date");
  }

  // 面板底部的示例行：四个选项的效果立刻可见（时间/日期、速度、距离）
  function prefPreview() {
    if (!window.PREFS) return "";
    return t("spPreview") + PREFS.sampleText();
  }

  // 改动设置后立刻重绘：单位标签、表盘读数、最近一次成绩、节点距离与示例行
  function applyPrefs() {
    var unit = speedUnit();
    var labels = document.querySelectorAll(".mt-unit");
    for (var i = 0; i < labels.length; i++) labels[i].textContent = unit;
    if (state.lastResult) {
      els.mtDown.textContent = fmtSpeed(state.lastResult.down);
      els.mtUp.textContent = fmtSpeed(state.lastResult.up);
      if (!state.running && els.dial.classList.contains("gauge")) {
        setDial(fmtSpeed(state.lastResult.down), unit, "", true);
        buildTicks(gaugeMax);
      }
    }
    renderSettings();
    if (els.modal && !els.modal.hidden) renderNodes();
  }

  function renderSettings() {
    if (!els.settingsPanel) return;
    readPrefsIntoUI();
    var c = clientOf(state.family);
    if (els.spIsp) {
      els.spIsp.textContent = ispLabelOf(c) || (c ? t("unknownIsp") : t("yourNetwork"));
    }
    if (els.spIp) els.spIp.textContent = c && c.ip ? ipLines(c.ip) : "--";
    if (els.spLoc) {
      var loc = c ? [c.city, c.region].filter(Boolean).join(" · ") : "";
      els.spLoc.textContent = loc || (c && c.ip ? "" : "--");
    }
    if (els.spGeo) {
      els.spGeo.textContent = c && (c.lat || c.lon)
        ? t("lat") + ": " + c.lat.toFixed(4) + "  " + t("lon") + ": " + c.lon.toFixed(4)
        : "";
    }
    var n = state.node;
    if (els.spNode) {
      els.spNode.textContent = n
        ? ((n.sponsor || "").trim() || (n.name || "").trim() || t("serverFallback"))
        : (state.ranking ? t("finding") : t("noServer"));
    }
    if (els.spNodeLoc) {
      var addr = n ? (nodeURL(n) || n.url || n.url6 || "") : "";
      var loc = n
        ? ((n.location || "").trim() ||
          (!state.isAdmin ? "" : (n.local && !addr ? t("thisServer") : hostOf(addr))))
        : "";
      var dist = nodeDistText(n);
      els.spNodeLoc.textContent = dist ? (loc ? loc + " · " + dist : dist) : loc;
    }
    if (els.spNodeProto) {
      els.spNodeProto.textContent = t("protocol") + " · " + (state.family === "v6" ? "IPv6" : "IPv4");
    }
    if (els.spChangeNode) els.spChangeNode.hidden = !state.settings.allowNodeSelect;
    if (els.spPreview) els.spPreview.textContent = prefPreview();
  }

  function toggleSettings(force) {
    if (!els.settingsPanel) return;
    var open = typeof force === "boolean" ? force : els.settingsPanel.hidden;
    if (open && state.running) return;
    els.settingsPanel.hidden = !open;
    document.body.classList.toggle("settings-open", open);
    els.settingsBtn.classList.toggle("on", open);
    if (open) renderSettings();
  }

  // 有多少协议可用：只有后台配了 IPv6 地址时才出现 IPv4 / IPv6 选项
  function familySupport() {
    var f = { v4: false, v6: false };
    state.nodes.forEach(function (n) {
      if (!n) return;
      if ((n.url || "").trim()) f.v4 = true;
      if ((n.url6 || "").trim()) f.v6 = true;
      if (n.local && !(n.url || "").trim() && !(n.url6 || "").trim()) { f.v4 = true; f.v6 = true; }
    });
    return f;
  }

  function renderProto() {
    var f = familySupport();
    if (!f.v6) state.family = "v4";
    else if (!f.v4) state.family = "v6";
    if (els.protoBlock) els.protoBlock.hidden = !(f.v4 && f.v6);
    document.querySelectorAll(".proto-opt").forEach(function (b) {
      b.classList.toggle("active", b.getAttribute("data-family") === state.family);
    });
  }

  function setFamily(fam) {
    if (fam !== "v6") fam = "v4";
    if (state.family === fam) { renderProto(); return; }
    state.family = fam;
    try { localStorage.setItem("st_family", fam); } catch (e) {}
    // 换了协议，之前测得的延迟作废；节点要重新按新协议自动挑
    state.nodes.forEach(function (n) { delete n.rtt; });
    state.ranking = true;
    state.picked = false;
    renderProto();
    syncPath();
    resetDial();
    renderClient();
    renderSettings();
    // 先补探该协议下的访客地址（决定本机节点的测速基址），再选节点排延迟
    probeClient(fam).then(function () {
      // loadPreferredNode 内部已经会测一遍延迟排序，不用再调一次
      loadPreferredNode().then(function (reachable) {
        if (fam === "v6" && reachable === 0 && state.nodes.length) toast(t("noNodeForFamily"));
      });
    });
  }

  function renderServer() {
    if (state.ranking && !state.picked) {
      els.serverName.textContent = t("finding");
      els.serverLoc.textContent = "";
      els.moreBtn.hidden = true;
      return;
    }
    var n = state.node;
    if (!n) {
      els.serverName.textContent = t("noServer");
      els.serverLoc.textContent = "";
      els.moreBtn.hidden = true;
      return;
    }
    // 第一行：服务商（后台「服务商名称」），第二行：地区（后台「地区名称」）
    var title = (n.sponsor || "").trim() || (n.name || "").trim() || t("serverFallback");
    var addr = nodeURL(n) || n.url || n.url6 || "";
    // 没填地区时下一步会退到显示地址 —— 这一层也只对管理员开放
    var loc = (n.location || "").trim();
    if (!loc && state.isAdmin) loc = n.local && !addr ? t("thisServer") : hostOf(addr);
    els.serverName.textContent = title;
    // 悬浮提示里也别带节点地址，只有管理员能看到
    els.serverName.title = state.isAdmin
      ? (n.name || "") + (addr ? " · " + hostOf(addr) : "")
      : (n.name || "");
    els.serverLoc.textContent = loc;
    var extra = availableNodes().length - 1;
    if (extra > 0 && state.settings.allowNodeSelect) {
      els.moreBtn.hidden = false;
      els.moreBtn.textContent = t("change");
      els.moreBtn.title = t("more", { n: extra });
    } else {
      els.moreBtn.hidden = true;
    }
  }

  function nodeKey() {
    return state.family === "v6" ? "st_node6" : "st_node";
  }

  function loadPreferredNode() {
    var pool = availableNodes();
    var pick = function (id) {
      if (!id) return null;
      for (var i = 0; i < pool.length; i++) if (pool[i].id === id) return pool[i];
      return null;
    };
    var saved = null;
    // 自动选点关闭时，上次记下的节点不该盖过后台的默认节点与顺序
    if (autoPick()) { try { saved = localStorage.getItem(nodeKey()); } catch (e) {} }
    // 顺序：上次用过的 → 该协议的默认节点 → 另一协议的默认节点 → 本机 / 第一个
    var node = pick(saved);
    if (!node) {
      var st = state.settings || {};
      node = pick(state.family === "v6" ? st.defaultNode6 : st.defaultNode) ||
        pick(state.family === "v6" ? st.defaultNode : st.defaultNode6);
    }
    if (!node) node = pool.filter(function (n) { return n.local; })[0] || pool[0] || null;
    state.node = node || null;
    state.base = node ? baseOf(node) : "";
    renderServer();
    return rankNodes();
  }

  function selectNode(node) {
    if (!node) return;
    state.node = node;
    state.base = baseOf(node);
    try { localStorage.setItem(nodeKey(), node.id); } catch (e) {}
    renderServer();
  }

  /* ---------------- 节点延迟排序 ---------------- */

  function pingOnce(base) {
    var t0 = performance.now();
    return fetch(base + "/api/ping?ts=" + Math.random().toString(36).slice(2), {
      cache: "no-store",
      mode: "cors"
    }).then(function (res) {
      return res.text().then(function () { return performance.now() - t0; });
    });
  }

  function measureNodeLatency(base) {
    var runs = [];
    var chain = Promise.resolve();
    for (var i = 0; i < 2; i++) {
      chain = chain.then(function () {
        return pingOnce(base).then(function (ms) { runs.push(ms); }).catch(function () {});
      });
    }
    return chain.then(function () {
      if (!runs.length) return Infinity;
      runs.sort(function (a, b) { return a - b; });
      return runs[0];
    });
  }

  // 阶段结束后的带载延迟：连打几次取中位数
  function measureLoadedLatency(base, n) {
    var runs = [];
    var chain = Promise.resolve();
    for (var i = 0; i < (n || 3); i++) {
      chain = chain.then(function () {
        return pingOnce(base).then(function (ms) {
          if (isFinite(ms) && ms > 0) runs.push(ms);
        }).catch(function () {});
      });
    }
    return chain.then(function () {
      if (!runs.length) return 0;
      runs.sort(function (a, b) { return a - b; });
      return runs[Math.floor(runs.length / 2)];
    });
  }

  // 测速阶段并行打点：下载/上传时各自的「带载延迟」，返回停止函数
  function probeLatency(base, onValue) {
    var alive = true;
    var runs = [];
    var tick = function () {
      if (!alive) return;
      pingOnce(base).then(function (ms) {
        if (!alive || !isFinite(ms) || ms <= 0) return;
        runs.push(ms);
        runs.sort(function (a, b) { return a - b; });
        onValue(runs[Math.floor(runs.length / 2)]);
      }).catch(function () {});
    };
    var timer = setInterval(tick, 1200);
    setTimeout(tick, 300);
    return function () {
      alive = false;
      clearInterval(timer);
    };
  }

  // 只在当前协议的节点里测延迟排序，返回可达节点数
  function rankNodes() {
    var pool = availableNodes();
    var fam = state.family;
    if (!pool.length) {
      state.ranking = false;
      renderServer();
      if (!els.modal.hidden) renderNodes();
      return Promise.resolve(0);
    }
    return Promise.all(pool.map(function (n) {
      return measureNodeLatency(baseOf(n)).then(function (ms) {
        n.rtt = ms;
        return n;
      });
    })).then(function (list) {
      // 期间切换了协议，这批结果作废（返回 null 不触发提示）
      if (state.family !== fam) return null;
      var live = list.filter(function (n) { return n.rtt !== Infinity; });
      // 开关关闭时只在后台顺序里待着，不按延迟重排、也不自动跳节点
      if (autoPick()) {
        list.sort(function (a, b) {
          return (a.rtt === Infinity ? 1e9 : a.rtt) - (b.rtt === Infinity ? 1e9 : b.rtt);
        });
        // 没手动选过就跟着延迟最低的走（含切换协议后的重新挑选）
        // 注意要取排序后的第一个，live 是排序前的原顺序
        var best = list.filter(function (n) { return n.rtt !== Infinity; })[0];
        if (best && !state.picked) selectNode(best);
      }
      state.ranking = false;
      renderServer();
      if (!els.modal.hidden) renderNodes();
      return live.length;
    });
  }

  /* ---------------- 延迟 ---------------- */

  // 延迟 / 抖动 / 丢包固定测 seconds 秒（默认 5 秒），期间尽量多打几次取中位数
  async function measurePing(base, seconds) {
    var samples = [];
    var attempts = 0;
    var fails = 0;
    var streak = 0;
    var budget = (seconds || 5) * 1000;
    var began = performance.now();
    var buckets = []; // 按秒分桶，报告页的曲线要显示每一秒的延迟
    for (var i = 0; i < 200; i++) {
      if (state.abort && state.abort.signal.aborted) break;
      if (performance.now() - began >= budget) break;
      attempts++;
      var t0 = performance.now();
      try {
        var res = await fetch(base + "/api/ping?ts=" + Math.random().toString(36).slice(2), {
          cache: "no-store",
          mode: "cors"
        });
        await res.text();
        var ms = performance.now() - t0;
        streak = 0;
        if (i > 0) {
          samples.push(ms);
          var slot = Math.floor((performance.now() - began) / 1000);
          if (slot < 0) slot = 0;
          (buckets[slot] || (buckets[slot] = [])).push(ms);
          els.pgPing.textContent = fmtPing(ms);
          setDial(fmtPing(ms), "ms", t("latency"), true);
          await new Promise(function (r) { setTimeout(r, 30); });
        }
      } catch (e) {
        fails++;
        streak++;
        if (streak >= 3) break;
      }
    }
    var loss = attempts ? Math.round((fails / attempts) * 100) : 0;
    if (!samples.length) return { ping: 0, jitter: 0, loss: loss, perSec: [] };
    var min = Math.min.apply(null, samples);
    var avg = samples.reduce(function (a, b) { return a + b; }, 0) / samples.length;
    var dev = samples.reduce(function (a, b) { return a + Math.abs(b - avg); }, 0) / samples.length;
    // 每秒取平均；某秒一次都没打上（收尾）就沿用前一秒，曲线不会突然掉到 0
    var perSec = [];
    var lastVal = 0;
    for (var k = 0; k < buckets.length && k < 8; k++) {
      var arr = buckets[k];
      var v = arr && arr.length
        ? arr.reduce(function (a, b) { return a + b; }, 0) / arr.length
        : (lastVal || avg);
      lastVal = v;
      perSec.push(v);
    }
    return { ping: min, jitter: dev, loss: loss, perSec: perSec };
  }

  /* ---------------- 下载 ---------------- */

  var WORKER_SRC = [
    "var stopped = false;",
    "self.onmessage = function (e) {",
    "  if (!e.data || e.data.cmd === 'stop') { stopped = true; return; }",
    "  var base = e.data.base, seconds = e.data.seconds, id = e.data.id;",
    "  var bytes = 0;",
    "  (async function () {",
    "    try {",
    "      var res = await fetch(base + '/api/download?seconds=' + seconds + '&r=' + Math.random().toString(36).slice(2), { cache: 'no-store' });",
    "      if (!res.ok || !res.body) { self.postMessage({ id: id, type: 'error' }); return; }",
    "      var reader = res.body.getReader();",
    "      while (!stopped) {",
    "        var chunk = await reader.read();",
    "        if (chunk.done) break;",
    "        bytes += chunk.value.byteLength;",
    "        self.postMessage({ id: id, type: 'bytes', bytes: bytes });",
    "      }",
    "      try { reader.cancel(); } catch (err) {}",
    "    } catch (err) {",
    "      self.postMessage({ id: id, type: 'error' });",
    "    }",
    "  })();",
    "};"
  ].join("\n");

  var workerUrl = null;
  function getWorkerUrl() {
    if (!workerUrl) workerUrl = URL.createObjectURL(new Blob([WORKER_SRC], { type: "application/javascript" }));
    return workerUrl;
  }

  function measureDownload(base, seconds, onSpeed) {
    var conns = connections();
    var counts = {};
    var workers = [];
    var duration = seconds * 1000;
    var start = performance.now();
    var lastTotal = 0;
    var samples = [{ t: 0, bytes: 0 }];

    // Blob Worker 的 base URL 是 blob:，必须使用绝对地址
    var absBase = /^https?:/i.test(base) ? base : location.origin + base;
    for (var i = 0; i < conns; i++) {
      (function (id) {
        var w = new Worker(getWorkerUrl());
        counts[id] = 0;
        w.onmessage = function (e) {
          if (e.data && e.data.type === "bytes") counts[id] = e.data.bytes;
        };
        w.postMessage({ cmd: "start", base: absBase, seconds: seconds, id: id });
        workers.push(w);
      })(i);
    }

    return new Promise(function (resolve) {
      var timer = setInterval(function () {
        var total = 0;
        for (var k in counts) total += counts[k];
        var dt = performance.now() - start;
        var done = dt >= duration || !!(state.abort && state.abort.signal.aborted);
        // 收尾按标称时长封顶：服务端断流后字节不再增长，这段停顿不该算进平均值
        samples.push({ t: done ? Math.min(dt, duration) : dt, bytes: total });
        lastTotal = total;
        if (!done) {
          var inst = dt > 1500 ? averageSpeed(samples, 10000, 900) : 0;
          if (inst > 0) onSpeed(inst);
        }
        if (done) {
          clearInterval(timer);
          workers.forEach(function (w) {
            try { w.postMessage({ cmd: "stop" }); } catch (e) {}
            try { w.terminate(); } catch (e) {}
          });
          // 收尾时再数一遍已到账的字节，报告里要展示实际消耗的流量
          var got = 0;
          for (var j in counts) got += counts[j];
          resolve({ samples: samples, bytes: Math.max(got, lastTotal) });
        }
      }, 250);
    });
  }

  /* ---------------- 上传 ---------------- */

  function makePayload(size) {
    var buf = new Uint8Array(size);
    var rnd = new Uint8Array(4096);
    if (window.crypto && window.crypto.getRandomValues) window.crypto.getRandomValues(rnd);
    for (var i = 0; i < size; i += rnd.length) buf.set(rnd, i);
    return new Blob([buf], { type: "application/octet-stream" });
  }

  function measureUpload(base, seconds, onSpeed, payloadBytes) {
    var conns = Math.min(connections(), 8);
    // 单块太大时，一条连接的已发字节是一段一段跳的，聚合起来就是明显的锯齿。
    // 1MB 一块，慢速线路上一块一两秒，多连接错开后正好被滑动窗口抹平；
    // 线路很快时再换成 4MB，免得请求数太多反而压不出带宽。
    var payload = makePayload(payloadBytes || 1024 * 1024);
    var per = [];   // 服务端已确认收下的字节
    var live = [];  // 当前这块正在发的进度（只用于统计总流量）
    for (var i = 0; i < conns; i++) { per.push(0); live.push(0); }
    var start = performance.now();
    var deadline = start + seconds * 1000;
    var lastTotal = 0;
    var samples = [{ t: 0, bytes: 0 }];
    var stopped = false;

    function loop(i) {
      if (stopped || performance.now() >= deadline) return Promise.resolve();
      if (state.abort && state.abort.signal.aborted) return Promise.resolve();
      return new Promise(function (resolve) {
        var xhr = new XMLHttpRequest();
        xhr.open("POST", base + "/api/upload?r=" + Math.random().toString(36).slice(2), true);
        xhr.timeout = 30000;
        xhr.setRequestHeader("Content-Type", "application/octet-stream");
        // 只认「服务端确认收下」的整块：浏览器上报的是写进 socket 缓冲的量，
        // 开局能瞬间报满一整块，读数就会先冲到 70 再掉回 35
        xhr.upload.onprogress = function (e) { live[i] = e.loaded; };
        xhr.onload = function () {
          if (xhr.status >= 200 && xhr.status < 300) per[i] += payload.size;
        };
        xhr.onloadend = function () { live[i] = 0; resolve(); };
        xhr.onerror = function () { live[i] = 0; resolve(); };
        xhr.send(payload);
      }).then(function () {
        return new Promise(function (r) { setTimeout(r, 0); });
      }).then(function () {
        return loop(i);
      });
    }

    var tasks = [];
    for (var c = 0; c < conns; c++) tasks.push(loop(c));

    var timer = setInterval(function () {
      var total = 0;
      for (var k = 0; k < per.length; k++) total += per[k];
      var dt = performance.now() - start;
      var done = dt >= seconds * 1000;
      // 同样按标称时长封顶，别把「请求已发完、等下一轮」的空档算成掉速
      samples.push({ t: done ? Math.min(dt, seconds * 1000) : dt, bytes: total });
      lastTotal = total;
      if (!done) {
        // 上传按整块确认计数，头一两块会在第 1~2 秒集中到账，先让这段过去再显示
        var inst = dt > 2500 ? averageSpeed(samples, 10000, 1800) : 0;
        if (inst > 0) onSpeed(inst);
      } else {
        stopped = true;
        clearInterval(timer);
      }
    }, 250);

    return Promise.all(tasks).then(function () {
      stopped = true;
      clearInterval(timer);
      return new Promise(function (resolve) {
        setTimeout(function () {
          // 上传总量 = 各连接已发出的字节之和，报告里要展示实际消耗的流量
          var sent = 0;
          for (var i = 0; i < per.length; i++) sent += per[i] + (live[i] || 0);
          resolve({ samples: samples, bytes: Math.max(sent, lastTotal) });
        }, 300);
      });
    });
  }

  /* 速率取「最近 windowMs 的平均」：前 15 秒里前面那段还在慢启动、后面才是稳态，
     所以报告和表盘都用最后 10 秒的平均值。区间被 warmMs 卡住，读数不会从 0 开始抖，
     而是单调爬到稳态后基本不动，也就不会出现「指针冲到 70、结果只有 45」的落差。 */
  function averageSpeed(samples, windowMs, warmMs) {
    if (!samples || samples.length < 2) return 0;
    var last = samples[samples.length - 1];
    var floor = Math.max(last.t - (windowMs || 10000), warmMs || 0);
    var base = samples[0];
    for (var i = 0; i < samples.length; i++) {
      if (samples[i].t >= floor) { base = samples[i]; break; }
    }
    var dt = last.t - base.t;
    if (dt < 500) return 0;
    return ((last.bytes - base.bytes) * 8) / (dt / 1000) / 1e6;
  }

  // 报告里的速度 = 最后 10 秒的平均值（测速总时长 15 秒，前 5 秒留给慢启动）
  function finalSpeed(samples) {
    return averageSpeed(samples, 10000, 0);
  }

  // 把 250ms 的采样按「秒」聚合：返回每一秒的平均速率（Mbps），下标 0 就是第 1 秒
  function perSecondSpeed(samples) {
    var acc = [];
    if (!samples) return acc;
    for (var i = 1; i < samples.length; i++) {
      var dt = samples[i].t - samples[i - 1].t;
      if (dt <= 0) continue;
      var sec = Math.floor(samples[i - 1].t / 1000);
      var e = acc[sec] || (acc[sec] = { bytes: 0, ms: 0 });
      var grew = samples[i].bytes - samples[i - 1].bytes;
      e.bytes += grew > 0 ? grew : 0;
      e.ms += dt;
    }
    var out = [];
    for (var k = 0; k < acc.length; k++) {
      var it = acc[k];
      if (!it || !it.ms) { out.push(0); continue; }
      out.push(Math.round(((it.bytes * 8) / (it.ms / 1000) / 1e6) * 100) / 100);
    }
    return out;
  }

  // 逐秒曲线：延迟（前 5 秒）→ 下行（15 秒）→ 上行（15 秒），每秒一个点，
  // 三个字段同时带上，报告页拖动时一眼看到那一秒的三项数据。
  function buildTimeline(ping, dlSamples, ulSamples) {
    var out = [];
    var pingSec = (ping && ping.perSec) || [];
    pingSec.forEach(function (v, i) {
      out.push({ t: i + 1, d: 0, u: 0, p: Math.round(v * 10) / 10 });
    });
    var base = pingSec.length;
    var dl = perSecondSpeed(dlSamples);
    dl.forEach(function (v, i) {
      out.push({ t: base + i + 1, d: v, u: 0, p: 0 });
    });
    base += dl.length;
    var ul = perSecondSpeed(ulSamples);
    ul.forEach(function (v, i) {
      out.push({ t: base + i + 1, d: 0, u: v, p: 0 });
    });
    return out.slice(0, 120);
  }

  /* ---------------- 主流程 ---------------- */

  function setRunning(on) {
    state.running = on;
    els.dial.classList.toggle("testing", on);
  }

  async function runTest() {
    if (state.running) {
      if (state.abort) state.abort.abort();
      setRunning(false);
      spin(false);
      resetDial();
      return;
    }
    if (!state.node) {
      toast(state.nodes.length ? t("noNodeForFamily") : t("pickServerFirst"));
      return;
    }

    var s = state.settings;
    toggleSettings(false);
    clearTimeout(state.jumpTimer);
    state.abort = new AbortController();
    gaugeMax = 1000;
    setRunning(true);
    els.dial.classList.remove("gauge");
    els.arc.style.strokeDashoffset = CIRC;
    enterGaugeMode();
    setDial("0.00", speedUnit(), "", true);

    var dl = 0;
    var ul = 0;
    var dlBytes = 0;
    var ulBytes = 0;

    try {
      var ping = await measurePing(state.base, 5);
      if (state.abort.signal.aborted) throw new Error("aborted");
      setCardProgress(12);

      var dlMeter = makeMeter(gaugeMax);
      setGauge(0, dlMeter.max());
      setDial("0.00", speedUnit(), "", true);
      var stopDlLat = probeLatency(state.base, function (ms) {
        els.pgDownLat.textContent = fmtPing(ms);
      });
      var dlRun = await measureDownload(state.base, s.downloadSeconds, dlMeter.tick);
      stopDlLat();
      // 下载占满连接时排队的小请求要等阶段结束才回来，这里再补测几次
      var dlLat = await measureLoadedLatency(state.base, 3);
      if (dlLat) els.pgDownLat.textContent = fmtPing(dlLat);
      dlBytes = dlRun.bytes || 0;
      if (state.abort.signal.aborted) throw new Error("aborted");
      dl = finalSpeed(dlRun.samples) || dl;
      gaugeMax = 1000;
      buildTicks(gaugeMax);
      setGauge(dl, gaugeMax);
      setDial(fmtSpeed(dl), speedUnit(), "", true);
      els.mtDown.textContent = fmtSpeed(dl);
      setCardProgress(55);

      setGauge(0, gaugeMax);
      setDial("0.00", speedUnit(), "", true);
      // 进入上传阶段：表盘与顶部图标切成上传色
      els.dial.classList.add("up");
      setStops("gaugeGrad", state.mode === "single" ? UP_GAUGE_COLORS.single : UP_GAUGE_COLORS.multi);
      if (els.mtUpItem) els.mtUpItem.classList.add("live");
      var ulMeter = makeMeter(gaugeMax);
      setGauge(0, ulMeter.max());
      setDial("0.00", speedUnit(), "", true);
      var stopUlLat = probeLatency(state.base, function (ms) {
        els.pgUpLat.textContent = fmtPing(ms);
      });
      var ulRun = await measureUpload(state.base, s.uploadSeconds, ulMeter.tick, dl > 300 ? 4 << 20 : 1 << 20);
      stopUlLat();
      var ulLat = await measureLoadedLatency(state.base, 3);
      if (ulLat) els.pgUpLat.textContent = fmtPing(ulLat);
      gaugeMax = 1000;
      ulBytes = ulRun.bytes || 0;
      ul = finalSpeed(ulRun.samples) || ul;
      setDial(fmtSpeed(ul), speedUnit(), "", true);
      els.mtUp.textContent = fmtSpeed(ul);
      setCardProgress(92);

      setRunning(false);
      els.dial.classList.remove("up");
      setStops("gaugeGrad", state.mode === "single" ? GAUGE_COLORS.single : GAUGE_COLORS.multi);
      buildTicks(gaugeMax);
      setGauge(dl, gaugeMax);
      setDial(fmtSpeed(dl), speedUnit(), "", true);
      setCardProgress(100);
      showResult(ping, dl, ul);
      reportResult(ping, dl, ul, dlBytes, ulBytes, buildTimeline(ping, dlRun.samples, ulRun.samples));
    } catch (e) {
      setRunning(false);
      spin(false);
      resetDial();
    }
  }

  function showResult(ping, down, up) {
    if (els.speedCard) { els.speedCard.classList.remove("testing"); els.speedCard.classList.add("result"); }
    els.pgPing.textContent = fmtPing(ping.ping);
    els.pgPing.textContent = fmtPing(ping.ping);
    els.mtDown.textContent = fmtSpeed(down);
    els.mtUp.textContent = fmtSpeed(up);
    renderExp(down, up, ping.ping);
    state.lastResult = { ping: ping, down: down, up: up };
  }

  // 报告链接带上语言与协议前缀：/zh-Hans|/en/ipv4|ipv6/result/<id>，
  // 分享出去一眼能看出这一轮走的是哪个协议，语言也跟着界面走。
  function resultPath(id) {
    var pre = window.I18N && window.I18N.prefix ? window.I18N.prefix() : "/zh-Hans";
    var r = routes();
    var vseg = state.family === "v6" ? (r.v6 || "ipv6") : (r.v4 || "ipv4");
    return pre + "/" + vseg + "/" + (r.result || "result") + "/" + id;
  }

  /* ---------------- 本机测速历史 ----------------
     只存在这台浏览器的 localStorage 里，不上传、不跨设备同步。
     老版本没有 st_hist，但 st_rc 里存着测速者凭证，首次打开时按编号回查报告补齐。 */

  var HIST_KEY = "st_hist";
  var HIST_CLEARED_KEY = "st_hist_cleared";

  function histEsc(s) {
    return String(s == null ? "" : s).replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
    });
  }

  function readHistory() {
    try {
      var a = JSON.parse(localStorage.getItem(HIST_KEY) || "[]");
      return Array.isArray(a) ? a.filter(function (x) { return x && x.id; }) : [];
    } catch (e) { return []; }
  }

  function writeHistory(list) {
    try { localStorage.setItem(HIST_KEY, JSON.stringify(list.slice(0, 30))); } catch (e) {}
  }

  function addHistory(e) {
    if (!e || !e.id) return;
    var list = readHistory().filter(function (x) { return x.id !== e.id; });
    list.push(e);
    writeHistory(list);
    try { localStorage.removeItem(HIST_CLEARED_KEY); } catch (err) {}
  }

  function histEntry(id, r) {
    return {
      id: id,
      time: r.time || "",
      down: Number(r.download) || 0,
      up: Number(r.upload) || 0,
      ping: Number(r.ping) || 0,
      family: r.ipVersion === "v6" ? "v6" : "v4",
      node: (r.node && (r.node.name || r.node.sponsor)) || ""
    };
  }

  // 用测速者凭证把老版本测过的报告找回来（最多 20 份），已删除的报告自动跳过
  function refillHistory() {
    var map = {};
    try { map = JSON.parse(localStorage.getItem("st_rc") || "{}") || {}; } catch (e) { map = {}; }
    var ids = Object.keys(map).slice(-20).reverse();
    if (!ids.length) return Promise.resolve();
    return Promise.all(ids.map(function (id) {
      var headers = map[id] ? { "X-Result-Owner": map[id] } : {};
      return fetch("/api/result/" + encodeURIComponent(id), { cache: "no-store", headers: headers })
        .then(function (res) { return res.ok ? res.json() : null; })
        .then(function (d) { return d && d.result ? histEntry(id, d.result) : null; })
        .catch(function () { return null; });
    })).then(function (found) {
      var list = readHistory();
      found.forEach(function (e) {
        if (e && !list.some(function (x) { return x.id === e.id; })) list.push(e);
      });
      list.sort(function (a, b) { return String(a.time).localeCompare(String(b.time)); });
      writeHistory(list);
    });
  }

  function renderHistory() {
    var box = $("histList"), count = $("histCount");
    if (!box) return;
    var list = readHistory().slice().sort(function (a, b) {
      return String(b.time).localeCompare(String(a.time));
    });
    if (count) count.textContent = list.length ? t("histCount", { n: list.length }) : "";
    if (!list.length) {
      box.innerHTML = '<div class="hist-empty">' + t("histEmpty") + "</div>";
      return;
    }
    var pre = window.I18N && window.I18N.prefix ? window.I18N.prefix() : "/zh-Hans";
    var unit = window.PREFS ? PREFS.speedUnit() : "Mbps";
    var sv = function (v) { return window.PREFS ? PREFS.speedText(v) : v.toFixed(2); };
    box.innerHTML = list.map(function (e) {
      var d = new Date(e.time);
      var when = isFinite(d.getTime()) && window.PREFS
        ? PREFS.fmtTime(Math.floor(d.getTime() / 1000), true)
        : String(e.time || "");
      var r = routes();
      var vseg = e.family === "v6" ? (r.v6 || "ipv6") : (r.v4 || "ipv4");
      var href = pre + "/" + vseg + "/" + (r.result || "result") + "/" + encodeURIComponent(e.id);
      return '<a class="hist-item" href="' + histEsc(href) + '">' +
        '<span class="hist-when">' + histEsc(when) + "</span>" +
        '<span class="hist-proto">' + (e.family === "v6" ? "IPv6" : "IPv4") + "</span>" +
        '<span class="hist-vals">' +
          '<b class="dl">' + histEsc(sv(e.down)) + ' <small>' + histEsc(unit) + " ↓</small></b>" +
          '<b class="ul">' + histEsc(sv(e.up)) + ' <small>' + histEsc(unit) + " ↑</small></b>" +
          '<b class="pg">' + histEsc(e.ping ? e.ping.toFixed(0) : "--") +
            ' <small>ms ' + histEsc(t("histPing")) + "</small></b>" +
        "</span>" +
        '<span class="hist-id">' + histEsc(t("histReport") + " #" + e.id +
          (e.node ? " · " + e.node : "")) + " ↗</span>" +
      "</a>";
    }).join("");
  }

  function openHistory() {
    var modal = $("historyModal");
    if (!modal) return;
    modal.hidden = false;
    var cleared = false;
    try { cleared = localStorage.getItem(HIST_CLEARED_KEY) === "1"; } catch (e) {}
    if (readHistory().length || cleared) { renderHistory(); return; }
    $("histList").innerHTML = '<div class="hist-empty">' + t("histLoading") + "</div>";
    refillHistory().then(function () { if (!modal.hidden) renderHistory(); });
  }

  function closeHistory() {
    var modal = $("historyModal");
    if (modal) modal.hidden = true;
  }

  // 记下这份报告的测速者凭证：同一浏览器打开报告页时凭它看到自己的 IP
  function saveOwnerToken(id, token) {
    if (!id || !token) return;
    var map = {};
    try { map = JSON.parse(localStorage.getItem("st_rc") || "{}") || {}; } catch (e) { map = {}; }
    map[id] = token;
    // 只留最近 40 份，localStorage 不会越堆越长
    var ids = Object.keys(map);
    if (ids.length > 40) {
      ids.slice(0, ids.length - 40).forEach(function (k) { delete map[k]; });
    }
    try { localStorage.setItem("st_rc", JSON.stringify(map)); } catch (e) {}
  }

  function reportResult(ping, down, up, downBytes, upBytes, timeline) {
    // 页面可能是用另一种协议打开的（例如走 IPv6 访问却选了 IPv4 节点），
    // 服务端从连接上只能看到那条协议的地址，所以把前端按所选协议探到的地址一并发过去。
    var me = clientOf(state.family);
    fetch("/api/report", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        nodeId: state.node ? state.node.id : "",
        nodeName: state.node ? state.node.name : "",
        ping: ping.ping,
        jitter: ping.jitter,
        download: down,
        upload: up,
        loss: ping.loss || 0,
        isp: state.isp || "",
        ispEn: state.ispEn || "",
        ipVersion: state.family,
        clientIp: (me && me.ip) || "",
        connections: connections(),
        downloadBytes: Math.round(downBytes || 0),
        uploadBytes: Math.round(upBytes || 0),
        timeline: timeline || []
      })
    })
      .then(function (r) { return r.json(); })
      .then(function (d) {
        if (!d || !d.id) return;
        state.resultId = d.id;
        saveOwnerToken(d.id, d.owner);
        addHistory({
          id: d.id,
          time: new Date().toISOString(),
          down: down,
          up: up,
          ping: ping.ping,
          family: state.family,
          node: state.node ? (state.node.name || "") : ""
        });
        els.resultId.textContent = d.id;
        els.openResult.href = resultPath(d.id);
        els.resultBar.hidden = false;
        // 与 speedtest 一致：测完自动进结果页，留一点时间看最终数值
        clearTimeout(state.jumpTimer);
        state.jumpTimer = setTimeout(function () {
          if (state.running || state.resultId !== d.id) return;
          location.href = resultPath(d.id);
        }, 1500);
      })
      .catch(function () {});
  }

  /* ---------------- 节点弹窗 ---------------- */

  function openModal() {
    if (!state.settings.allowNodeSelect || state.running) return;
    renderNodes();
    els.modal.hidden = false;
    rankNodes();
  }

  function closeModal() {
    els.modal.hidden = true;
  }

  function nodeFamilyTags(n) {
    var out = [];
    if ((n.url || "").trim()) out.push("IPv4");
    if ((n.url6 || "").trim()) out.push("IPv6");
    if (!out.length) return "";
    return out.join(" + ");
  }

  function renderNodes() {
    els.nodeList.innerHTML = "";
    var list = availableNodes().slice();
    if (autoPick()) {
      list.sort(function (a, b) {
        return (a.rtt || 1e9) - (b.rtt || 1e9);
      });
    }
    if (!list.length) {
      els.nodeList.innerHTML = '<div class="node-meta" style="padding:16px">' +
        (state.nodes.length ? t("noNodeForFamily") : t("noServersInList")) + "</div>";
      return;
    }
    // 顶部一行说明：只列当前协议的节点，并按延迟排序自动挑最低的
    var head = document.createElement("div");
    head.className = "node-head";
    head.textContent = t(autoPick() ? "nodeAutoHint" : "nodeOrderHint", {
      proto: state.family === "v6" ? "IPv6" : "IPv4",
      n: list.length
    });
    els.nodeList.appendChild(head);
    var bestId = autoPick() && list.length && typeof list[0].rtt === "number" && isFinite(list[0].rtt)
      ? list[0].id : "";
    list.forEach(function (n) {
      var row = document.createElement("div");
      row.className = "node-row" + (state.node && n.id === state.node.id ? " selected" : "");

      var main = document.createElement("div");
      main.className = "node-main";
      var name = document.createElement("div");
      name.className = "node-name";
      name.textContent = (n.sponsor || "").trim() || n.name;
      var fam = nodeFamilyTags(n);
      if (fam) {
        var famTag = document.createElement("span");
        famTag.className = "node-fam";
        famTag.textContent = fam;
        name.appendChild(famTag);
      }
      if (n.id === bestId) {
        var bestTag = document.createElement("span");
        bestTag.className = "node-best";
        bestTag.textContent = t("lowestPing");
        name.appendChild(bestTag);
      }
      var meta = document.createElement("div");
      meta.className = "node-meta";
      // 节点地址只给管理员看，普通访客只看到地区与名称
      var bits = [n.location, n.name];
      if (state.isAdmin) bits.push(nodeURL(n) || t("thisServer"));
      var nd = nodeDistText(n);
      if (nd) bits.push(nd);
      meta.textContent = bits.filter(Boolean).join(" · ");
      main.appendChild(name);
      main.appendChild(meta);

      var chip = document.createElement("div");
      chip.className = "node-rtt";
      if (typeof n.rtt === "number" && isFinite(n.rtt)) {
        chip.textContent = Math.round(n.rtt) + " ms";
        if (n.rtt < 60) chip.classList.add("good");
      } else if (n.rtt === Infinity) {
        chip.textContent = t("unavailable");
      } else {
        chip.textContent = "…";
      }

      row.appendChild(main);
      row.appendChild(chip);
      row.addEventListener("click", function () {
        state.picked = true;
        selectNode(n);
        renderNodes();
        closeModal();
        resetDial();
      });
      els.nodeList.appendChild(row);
    });
  }

  /* ---------------- 事件 ---------------- */

  // 浏览器前进/后退改了路径时，跟着切协议
  window.addEventListener("popstate", function () {
    var f = pathFamily();
    if (f && f !== state.family) setFamily(f);
  });

  els.dial.addEventListener("click", runTest);
  els.dial.addEventListener("keydown", function (e) {
    if (e.key === "Enter" || e.key === " ") { e.preventDefault(); runTest(); }
  });
  if (els.clientIco) els.clientIco.addEventListener("click", openModal);
  if (els.moreBtn) els.moreBtn.addEventListener("click", openModal);
  els.serverItem.addEventListener("click", openModal);
  els.serverItem.addEventListener("keydown", function (e) {
    if (e.key === "Enter" || e.key === " ") { e.preventDefault(); openModal(); }
  });
  els.settingsBtn.addEventListener("click", function () { toggleSettings(); });
  if (els.spChangeNode) els.spChangeNode.addEventListener("click", openModal);
  if (els.settingsPanel) {
    els.settingsPanel.addEventListener("click", function (e) {
      var btn = e.target.closest ? e.target.closest(".sp-opt") : null;
      if (!btn) return;
      var group = btn.closest(".sp-opts");
      if (!group || !window.PREFS) return;
      PREFS.set(group.getAttribute("data-pref"), btn.getAttribute("data-val"));
      readPrefsIntoUI();
      applyPrefs();
    });
    els.settingsPanel.addEventListener("change", function (e) {
      var sel = e.target;
      if (!sel || !sel.getAttribute || !sel.getAttribute("data-pref") || !window.PREFS) return;
      PREFS.set(sel.getAttribute("data-pref"), sel.value);
      readPrefsIntoUI();
      applyPrefs();
    });
  }
  els.closeModal.addEventListener("click", closeModal);
  els.modal.addEventListener("click", function (e) {
    if (e.target === els.modal) closeModal();
  });

  els.copyResult.addEventListener("click", function () {
    if (!state.resultId) return;
    var url = location.origin + resultPath(state.resultId);
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(url).then(function () { toast(t("linkCopied")); })
        .catch(function () { toast(url); });
    } else {
      toast(url);
    }
  });

  // 「结果」打开这台设备历次测速的历史列表，每条都能点进对应报告
  els.resultsBtn.addEventListener("click", openHistory);

  $("histClose").addEventListener("click", closeHistory);
  $("historyModal").addEventListener("click", function (e) {
    if (e.target === $("historyModal")) closeHistory();
  });
  $("histClear").addEventListener("click", function () {
    writeHistory([]);
    try { localStorage.setItem(HIST_CLEARED_KEY, "1"); } catch (e) {}
    renderHistory();
    toast(t("histCleared"));
  });

  // 首页「管理后台」是摆设：保留外观，但不跳转、不弹窗，HTML 里也没有真实入口
  Array.prototype.forEach.call(document.querySelectorAll("[data-fake-admin]"), function (b) {
    b.addEventListener("click", function (e) { e.preventDefault(); e.stopPropagation(); });
  });

  if (els.langBtn) {
    els.langBtn.addEventListener("click", function (e) {
      e.stopPropagation();
      var open = els.langPanel.hidden;
      els.langPanel.hidden = !open;
      els.langBtn.setAttribute("aria-expanded", open ? "true" : "false");
    });

    els.langPanel.addEventListener("click", function (e) {
      var target = e.target.closest ? e.target.closest("[data-lang]") : null;
      if (!target) return;
      window.I18N.set(target.getAttribute("data-lang"));
      els.langPanel.hidden = true;
      els.langBtn.setAttribute("aria-expanded", "false");
    });
  }

  document.addEventListener("langchange", function () {
    applySettings();
    renderSettings();
    layoutClientIP();
    if ($("historyModal") && !$("historyModal").hidden) renderHistory();
    if (!state.running && !els.dial.classList.contains("gauge")) setDial("GO", "", "", false);
  });

  // 窗口宽度变化会改字体大小 / 折行，徽标宽度跟着变，重新算一次 IP 的缩进
  window.addEventListener("resize", layoutClientIP);

  els.menuBtn.addEventListener("click", function (e) {
    e.stopPropagation();
    els.menuPanel.hidden = !els.menuPanel.hidden;
  });

  els.menuPanel.addEventListener("click", function (e) {
    var act = e.target.getAttribute("data-act");
    if (act === "results") els.resultsBtn.click();
    if (act === "settings") toggleSettings(true);
    els.menuPanel.hidden = true;
  });

  document.addEventListener("click", function () {
    els.menuPanel.hidden = true;
    if (els.langPanel) {
      els.langPanel.hidden = true;
      els.langBtn.setAttribute("aria-expanded", "false");
    }
  });

  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape") {
      if (!els.modal.hidden) closeModal();
      closeHistory();
      toggleSettings(false);
      els.menuPanel.hidden = true;
      if (els.langPanel) els.langPanel.hidden = true;
    }
  });

  Array.prototype.forEach.call(document.querySelectorAll(".proto-opt"), function (btn) {
    btn.addEventListener("click", function () {
      if (state.running) return;
      setFamily(btn.getAttribute("data-family"));
    });
  });

  Array.prototype.forEach.call(document.querySelectorAll(".mode-opt"), function (btn) {
    btn.addEventListener("click", function () {
      if (state.running) return;
      var mode = btn.getAttribute("data-mode");
      state.mode = mode;
      document.querySelectorAll(".mode-opt").forEach(function (b) {
        b.classList.toggle("active", b === btn);
      });
      try { localStorage.setItem("st_mode", mode); } catch (e) {}
      applyMode();
      resetDial();
    });
  });

  if (els.modeToggle) {
    els.modeToggle.addEventListener("click", function () {
      if (state.running) return;
      var next = state.mode === "single" ? "multi" : "single";
      var btn = document.querySelector('.mode-opt[data-mode="' + next + '"]');
      if (btn) btn.click();
    });
  }

  (function initMode() {
    var saved = null;
    try { saved = localStorage.getItem("st_mode"); } catch (e) {}
    if (saved === "single" || saved === "multi") {
      state.mode = saved;
      document.querySelectorAll(".mode-opt").forEach(function (b) {
        b.classList.toggle("active", b.getAttribute("data-mode") === saved);
      });
    }
    applyMode();
  })();

  (function initFamily() {
    var saved = null;
    try { saved = localStorage.getItem("st_family"); } catch (e) {}
    if (saved === "v6") state.family = "v6";
  })();

  boot();
})();
