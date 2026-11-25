(function () {
  "use strict";

  var $ = function (id) { return document.getElementById(id); };

  var state = { nodes: [], settings: null, results: [], username: "", calNode: null };

  /* ---------- 基础 ---------- */

  function toast(msg, isError) {
    var el = $("toast");
    el.textContent = msg;
    el.className = "toast show" + (isError ? " error" : "");
    clearTimeout(el._t);
    el._t = setTimeout(function () { el.className = "toast"; }, 2600);
  }

  function api(path, opts) {
    opts = opts || {};
    var init = {
      method: opts.method || "GET",
      headers: {},
      credentials: "same-origin"
    };
    if (opts.body !== undefined) {
      init.headers["Content-Type"] = "application/json";
      init.body = JSON.stringify(opts.body);
    }
    return fetch(path, init).then(function (res) {
      return res.json().catch(function () { return {}; }).then(function (data) {
        if (!res.ok || data.ok === false) {
          var err = new Error(data.error || ("请求失败 (" + res.status + ")"));
          err.status = res.status;
          throw err;
        }
        return data;
      });
    });
  }

  function showLogin() {
    $("loginView").hidden = false;
    $("appView").hidden = true;
    $("logoutBtn").hidden = true;
  }

  // 默认密码提醒：mustChange 为 true 时常驻顶部横幅，改完密码自动消失
  function checkPwdWarn() {
    var box = $("pwdWarn");
    if (!box) return;
    api("/api/admin/me").then(function (d) {
      box.hidden = !d.mustChange;
    }).catch(function () {});
  }

  function showApp() {
    $("loginView").hidden = true;
    $("appView").hidden = false;
    $("logoutBtn").hidden = false;
    checkPwdWarn();
    loadOverview();
    loadNodes();
  }

  /* ---------- 登录 ---------- */

  $("loginForm").addEventListener("submit", function (e) {
    e.preventDefault();
    var btn = $("loginBtn");
    $("loginErr").textContent = "";
    btn.disabled = true;
    api("/api/admin/login", {
      method: "POST",
      body: { username: $("username").value.trim(), password: $("password").value }
    }).then(function () {
      $("password").value = "";
      showApp();
    }).catch(function (err) {
      $("loginErr").textContent = err.message;
    }).then(function () {
      btn.disabled = false;
    });
  });

  $("logoutBtn").addEventListener("click", function () {
    api("/api/admin/logout", { method: "POST" }).then(showLogin).catch(showLogin);
  });

  $("pwdWarn").addEventListener("click", function () {
    var tab = document.querySelector('.tab[data-tab="account"]');
    if (tab) tab.click();
    var el = $("accOld");
    if (el) el.focus();
  });

  /* ---------- 标签页 ---------- */

  Array.prototype.forEach.call(document.querySelectorAll(".tab"), function (tab) {
    tab.addEventListener("click", function () {
      document.querySelectorAll(".tab").forEach(function (t) { t.classList.remove("active"); });
      tab.classList.add("active");
      var name = tab.getAttribute("data-tab");
      ["overview", "nodes", "settings", "account"].forEach(function (key) {
        $("panel-" + key).hidden = key !== name;
      });
      if (name === "overview") loadOverview();
      if (name === "nodes") loadNodes();
    });
  });

  /* ---------- 概览 ---------- */

  function fmtUptime(sec) {
    if (!sec && sec !== 0) return "-";
    var d = Math.floor(sec / 86400);
    var h = Math.floor((sec % 86400) / 3600);
    var m = Math.floor((sec % 3600) / 60);
    if (d > 0) return d + "d " + h + "h";
    if (h > 0) return h + "h " + m + "m";
    return m + "m";
  }

  function loadOverview() {
    api("/api/admin/overview").then(function (data) {
      state.nodes = data.nodes || [];
      state.settings = data.settings;
      state.username = data.username;
      $("headSite").textContent = (data.settings && data.settings.siteName) || "SpeedTest";
      $("kpiNodes").textContent = state.nodes.length;
      // 所有节点本月流量合计：主数字按各节点自己的口径折算后的用量，副行给上下行原始量
      var sumUsed = 0, sumDown = 0, sumUp = 0;
      state.nodes.forEach(function (n) {
        sumUsed += Number(n.trafficUsed) || 0;
        sumDown += Number(n.trafficDown) || 0;
        sumUp += Number(n.trafficUp) || 0;
      });
      $("kpiTraffic").textContent = (sumUsed || sumDown || sumUp) ? fmtSize(sumUsed) : "-";
      $("kpiTrafficSub").textContent = (sumDown || sumUp)
        ? "下行 " + fmtSize(sumDown) + " · 上行 " + fmtSize(sumUp)
        : "暂无流量";
      $("kpiUptime").textContent = fmtUptime(data.uptime);
      $("kpiVersion").innerHTML = data.version + "<small>v</small>";
      fillSettings(state.settings);
      loadResults();
    }).catch(function (err) {
      if (err.status === 401) showLogin();
    });
  }

  function loadResults() {
    api("/api/admin/results?limit=50").then(function (data) {
      state.results = data.results || [];
      $("kpiResults").textContent = state.results.length;
      renderResults();
    }).catch(function () {});
  }

  function renderResults() {
    var box = $("resultsBox");
    if (!state.results.length) {
      box.innerHTML = '<div class="empty">还没有测速记录</div>';
      return;
    }
    var rows = state.results.map(function (r) {
      var time = new Date(r.time);
      var ts = time.toLocaleString("zh-CN", { hour12: false });
      // 每条记录都能点进对应的测速报告页（路径段来自后台配置，协议跟着 IP 版本走）
      var paths = window.ST_ROUTES || {};
      var href = "/" + (paths.langZh || "zh-Hans") + "/" +
        (r.ipVersion === "v6" ? (paths.v6 || "ipv6") : (paths.v4 || "ipv4")) +
        "/" + (paths.result || "result") + "/" + encodeURIComponent(r.id || "");
      return "<tr class='clickable' data-href='" + href + "'>" +
        "<td class='mono'>" + ts + "</td>" +
        "<td>" + esc(r.nodeName || "-") + "</td>" +
        "<td class='mono'>" + esc(r.ip || "-") + "</td>" +
        "<td>" + (r.ping ? r.ping.toFixed(0) : "-") + " ms</td>" +
        "<td>" + (r.download ? r.download.toFixed(2) : "-") + "</td>" +
        "<td>" + (r.upload ? r.upload.toFixed(2) : "-") + "</td>" +
        "<td><a class='rlink mono' href='" + href + "' target='_blank' rel='noopener'>" +
        esc(r.id || "-") + "</a></td>" +
        "</tr>";
    }).join("");
    box.innerHTML = "<table class='tbl'><thead><tr>" +
      "<th>时间</th><th>节点</th><th>访客 IP</th><th>延迟</th><th>下载 Mbps</th><th>上传 Mbps</th><th>报告</th>" +
      "</tr></thead><tbody>" + rows + "</tbody></table>";
  }

  $("refreshResults").addEventListener("click", loadResults);

  // 整行可点，直接打开对应的测速报告页；链接本身保持默认行为（可新标签打开）
  $("resultsBox").addEventListener("click", function (e) {
    if (e.target.closest && e.target.closest("a")) return;
    var tr = e.target.closest ? e.target.closest("tr[data-href]") : null;
    if (!tr) return;
    window.open(tr.getAttribute("data-href"), "_blank", "noopener");
  });

  /* ---------- 节点 ---------- */

  function esc(s) {
    return String(s == null ? "" : s).replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
    });
  }

  function loadNodes() {
    api("/api/admin/nodes").then(function (data) {
      state.nodes = data.nodes || [];
      renderNodes();
      fillDefaultNodeSelect();
    }).catch(function (err) {
      if (err.status === 401) showLogin();
    });
  }

  function renderNodes() {
    var box = $("nodesBox");
    if (!state.nodes.length) {
      box.innerHTML = '<div class="empty">还没有节点</div>';
      return;
    }
    var rows = state.nodes.map(function (n) {
      var tags = [];
      if (n.local) tags.push('<span class="tag local">本机</span>');
      tags.push(n.enabled ? '<span class="tag on">启用</span>' : '<span class="tag off">停用</span>');
      if (n.trafficBlocked) tags.push('<span class="tag off">超限</span>');
      // 一个节点可以同时有 IPv4 / IPv6 两个接入地址
      var addr = [];
      if (n.url) addr.push("<div class='mono'>IPv4 " + esc(n.url) + "</div>");
      if (n.url6) addr.push("<div class='mono'>IPv6 " + esc(n.url6) + "</div>");
      if (!addr.length) addr.push("<div class='mono'>本机地址</div>");
      var used = Number(n.trafficUsed) || 0;
      var canReset = used > 0 || !!n.trafficBlocked;
      return "<tr>" +
        "<td><div>" + esc(n.name) + "</div><div class='mono'>" + tags.join(" ") + "</div></td>" +
        "<td>" + esc(n.location || "-") + "</td>" +
        "<td>" + esc(n.sponsor || "-") + "</td>" +
        "<td>" + addr.join("") + "</td>" +
        "<td>" + trafficCell(n) + "</td>" +
        "<td><div class='row-actions'>" +
        "<button class='a-btn small' data-act='probe' data-id='" + esc(n.id) + "'>测试</button>" +
        "<button class='a-btn small' data-act='edit' data-id='" + esc(n.id) + "'>编辑</button>" +
        (canReset ? "<button class='a-btn small' data-act='resetTraffic' data-id='" + esc(n.id) + "'>清零</button>" : "") +
        "<button class='a-btn small' data-act='calTraffic' data-id='" + esc(n.id) + "'>校准</button>" +
        (n.local ? "" : "<button class='a-btn small danger' data-act='del' data-id='" + esc(n.id) + "'>删除</button>") +
        "</div></td></tr>";
    }).join("");
    box.innerHTML = "<table class='tbl'><thead><tr>" +
      "<th>节点</th><th>地区</th><th>运营方</th><th>地址</th><th>本月流量</th><th style='text-align:right'>操作</th>" +
      "</tr></thead><tbody>" + rows + "</tbody></table>";

    box.querySelectorAll("button[data-act]").forEach(function (btn) {
      btn.addEventListener("click", function () {
        var id = btn.getAttribute("data-id");
        var act = btn.getAttribute("data-act");
        var node = state.nodes.filter(function (n) { return n.id === id; })[0];
        if (!node) return;
        if (act === "edit") openNodeModal(node);
        if (act === "del") removeNode(node);
        if (act === "probe") probe(node);
        if (act === "resetTraffic") resetTraffic(node);
        if (act === "calTraffic") openTrafficModal(node);
      });
    });
  }

  // 字节数转成人类可读；后台流量一律按 GiB（1024^3）换算，与限额口径保持一致
  function fmtSize(bytes) {
    var b = Number(bytes) || 0;
    if (b >= 1073741824) return (b / 1073741824).toFixed(2) + " GB";
    if (b >= 1048576) return (b / 1048576).toFixed(1) + " MB";
    return (b / 1024).toFixed(0) + " KB";
  }

  var TRAFFIC_MODE_LABEL = { sum: "上下行合计", down: "仅下行", up: "仅上行", max: "取较大值" };

  function trafficCell(n) {
    var used = Number(n.trafficUsed) || 0;
    var down = Number(n.trafficDown) || 0;
    var up = Number(n.trafficUp) || 0;
    var limitGB = Number(n.trafficLimitGB) || 0;
    var mode = n.trafficMode || "sum";
    if (!limitGB && !used && !down && !up) return "<span class='mono dim'>不限</span>";
    var html = "";
    if (limitGB > 0) {
      var pct = Math.min(100, (used / (limitGB * 1073741824)) * 100);
      var cls = pct >= 100 ? " over" : (pct >= 80 ? " warn" : "");
      html += "<div class='q-bar" + cls + "'><i style='width:" + pct.toFixed(1) + "%'></i></div>" +
        "<div class='mono'>" + fmtSize(used) + " / " + limitGB + " GB</div>";
    } else {
      html += "<div class='mono'>" + fmtSize(used) + "</div>";
    }
    // 上下行原始用量：用来核对云厂商账单，也是切换口径后重算的依据
    if (down || up) {
      html += "<div class='mono dim'>下行 " + fmtSize(down) + " · 上行 " + fmtSize(up) + "</div>";
    }
    if (mode !== "sum" && (used || down || up)) {
      html += "<div class='mono dim'>按" + (TRAFFIC_MODE_LABEL[mode] || "合计") + "计</div>";
    }
    if (limitGB > 0 && Number(n.trafficPeriodDay) > 0) {
      html += "<div class='mono dim'>每月 " + Number(n.trafficPeriodDay) + " 号重置" +
        (n.trafficAutoResume ? "并启用" : "") + "</div>";
    }
    if (n.trafficBlocked) html += "<div class='mono over-text'>已达限额，已停用</div>";
    return html;
  }

  // 字节转 GB 字符串：保留 4 位小数并去掉多余的 0。
  // 不能用 toFixed(2)：1 MB 会变成 "0.00"，用户一保存就把用量清零了。
  function bytesToGBText(bytes) {
    var gb = (Number(bytes) || 0) / 1073741824;
    if (!gb) return "0";
    var s = gb.toFixed(4);
    if (s.indexOf(".") >= 0) s = s.replace(/0+$/, "").replace(/\.$/, "");
    return s;
  }

  /* ---------- 校准已用流量 ---------- */
  function openTrafficModal(node) {
    state.calNode = node;
    $("trafficErr").textContent = "";
    $("trafficNodeName").textContent = node.name + (node.local ? "（本机）" : "");
    // 预填当前值（GB），方便在原值上微调
    $("trafficDown").value = bytesToGBText(node.trafficDown);
    $("trafficUp").value = bytesToGBText(node.trafficUp);
    $("trafficModal").hidden = false;
    $("trafficDown").focus();
  }

  $("trafficCancel").addEventListener("click", function () { $("trafficModal").hidden = true; });
  $("trafficModal").addEventListener("click", function (e) {
    if (e.target === $("trafficModal")) $("trafficModal").hidden = true;
  });

  $("trafficForm").addEventListener("submit", function (e) {
    e.preventDefault();
    var node = state.calNode;
    if (!node) return;
    var down = parseFloat($("trafficDown").value) || 0;
    var up = parseFloat($("trafficUp").value) || 0;
    if (down < 0 || up < 0) { $("trafficErr").textContent = "用量不能为负数"; return; }
    api("/api/admin/nodes/" + encodeURIComponent(node.id) + "/traffic/set", {
      method: "POST",
      body: { downGB: down, upGB: up }
    }).then(function (d) {
      $("trafficModal").hidden = true;
      var n = d && d.node;
      var msg = "流量已校准";
      if (n && n.trafficBlocked) msg += "；已达到限额，节点已停用";
      toast(msg);
      loadNodes();
      loadOverview();
    }).catch(function (err) {
      $("trafficErr").textContent = err.message;
    });
  });

  // 手动清零：默认顺带启用节点，否则清完了它还是停用状态，容易误以为没生效
  function resetTraffic(node) {
    if (!confirm("把「" + node.name + "」本月已用流量清零，并重新启用该节点？")) return;
    api("/api/admin/nodes/" + encodeURIComponent(node.id) + "/traffic/reset", {
      method: "POST",
      body: { resume: true }
    }).then(function () {
      toast("流量已清零");
      loadNodes();
      loadOverview();
    }).catch(function (err) {
      toast(err.message);
    });
  }

  // 某个节点是否可用于指定协议（本机节点两个地址都空时视为可用）
  function nodeHasFamily(n, fam) {
    var explicit = fam === "v6" ? (n.url6 || "").trim() : (n.url || "").trim();
    if (explicit) return true;
    return !!n.local && !(n.url || "").trim() && !(n.url6 || "").trim();
  }

  function fillDefaultNodeSelect() {
    [["setDefaultNode", "defaultNode", "v4"], ["setDefaultNode6", "defaultNode6", "v6"]].forEach(function (item) {
      var sel = $(item[0]);
      if (!sel) return;
      var current = state.settings ? state.settings[item[1]] : "";
      var opts = ["<option value=''>自动（延迟最低）</option>"];
      state.nodes.filter(function (n) { return nodeHasFamily(n, item[2]); }).forEach(function (n) {
        opts.push("<option value='" + esc(n.id) + "'>" + esc(n.name) + "</option>");
      });
      sel.innerHTML = opts.join("");
      sel.value = current || "";
      if (sel.selectedIndex < 0) sel.selectedIndex = 0;
    });
  }

  function openNodeModal(node) {
    $("nodeErr").textContent = "";
    $("nodeModal").hidden = false;
    $("nodeModalTitle").textContent = node ? "编辑节点" : "添加测速节点";
    $("nodeId").value = node ? node.id : "";
    $("nodeName").value = node ? node.name : "";
    $("nodeLocation").value = node ? node.location || "" : "";
    $("nodeSponsor").value = node ? node.sponsor || "" : "";
    $("nodeUrl").value = node ? node.url || "" : "";
    $("nodeUrl6").value = node ? node.url6 || "" : "";
    $("nodeLat").value = node && node.lat ? String(node.lat) : "";
    $("nodeLon").value = node && node.lon ? String(node.lon) : "";
    $("nodeEnabled").checked = node ? !!node.enabled : true;
    $("nodeQuota").value = node && node.trafficLimitGB ? String(node.trafficLimitGB) : "";
    $("nodeQuotaDay").value = node && node.trafficPeriodDay ? String(node.trafficPeriodDay) : "";
    $("nodeQuotaResume").checked = node ? !!node.trafficAutoResume : true;
    $("nodeQuotaMode").value = node ? (node.trafficMode || "sum") : "sum";
    $("probeBtn").style.display = "";
  }

  $("addNodeBtn").addEventListener("click", function () { openNodeModal(null); });
  $("nodeCancel").addEventListener("click", function () { $("nodeModal").hidden = true; });
  $("nodeModal").addEventListener("click", function (e) {
    if (e.target === $("nodeModal")) $("nodeModal").hidden = true;
  });

  $("nodeForm").addEventListener("submit", function (e) {
    e.preventDefault();
    var id = $("nodeId").value;
    var body = {
      name: $("nodeName").value.trim(),
      location: $("nodeLocation").value.trim(),
      sponsor: $("nodeSponsor").value.trim(),
      url: $("nodeUrl").value.trim(),
      url6: $("nodeUrl6").value.trim(),
      lat: parseFloat($("nodeLat").value) || 0,
      lon: parseFloat($("nodeLon").value) || 0,
      trafficLimitGB: parseFloat($("nodeQuota").value) || 0,
      trafficPeriodDay: parseInt($("nodeQuotaDay").value, 10) || 0,
      trafficAutoResume: $("nodeQuotaResume").checked,
      trafficMode: $("nodeQuotaMode").value,
      enabled: $("nodeEnabled").checked
    };
    if (!body.name) { $("nodeErr").textContent = "请填写节点名称"; return; }
    var req = id
      ? api("/api/admin/nodes/" + encodeURIComponent(id), { method: "PUT", body: body })
      : api("/api/admin/nodes", { method: "POST", body: body });
    req.then(function () {
      $("nodeModal").hidden = true;
      toast("节点已保存");
      loadNodes();
      loadOverview();
    }).catch(function (err) {
      $("nodeErr").textContent = err.message;
    });
  });

  // 逐个地址探测，返回 [{label, ok, text}]
  function probeURLs(list) {
    var out = [];
    var chain = Promise.resolve();
    list.forEach(function (item) {
      chain = chain.then(function () {
        return api("/api/admin/nodes/probe", { method: "POST", body: { url: item.url } })
          .then(function (data) {
            out.push({ label: item.label, ok: true, url: data.url, ms: data.ms });
          })
          .catch(function (err) {
            out.push({ label: item.label, ok: false, text: err.message });
          });
      });
    });
    return chain.then(function () { return out; });
  }

  function probeTargets() {
    var list = [];
    var u4 = $("nodeUrl").value.trim();
    var u6 = $("nodeUrl6").value.trim();
    if (u4) list.push({ label: "IPv4", url: u4 });
    if (u6) list.push({ label: "IPv6", url: u6 });
    return list;
  }

  $("probeBtn").addEventListener("click", function () {
    var list = probeTargets();
    if (!list.length) { $("nodeErr").textContent = "请先填写节点地址"; return; }
    $("nodeErr").textContent = "正在测试…";
    probeURLs(list).then(function (res) {
      var okCount = 0;
      var texts = [];
      res.forEach(function (r) {
        if (r.ok) {
          okCount++;
          texts.push(r.label + " 正常（" + r.ms.toFixed(0) + " ms）");
          if (r.label === "IPv4") $("nodeUrl").value = r.url;
          if (r.label === "IPv6") $("nodeUrl6").value = r.url;
        } else {
          texts.push(r.label + " 失败：" + r.text);
        }
      });
      $("nodeErr").textContent = texts.join("　");
      toast(okCount === list.length ? "节点可用" : "部分地址不可达", okCount !== list.length);
    });
  });

  // 访客地址解析检测：把源站看到的转发头原样列出来，换 CDN 时对着看就行
  if ($("btnIpDebug")) {
    $("btnIpDebug").addEventListener("click", function () {
      var out = $("ipDebugOut");
      out.hidden = false;
      out.textContent = "检测中…";
      api("/api/admin/ipdebug").then(function (d) {
        var lines = [];
        lines.push("解析结果：" + (d.resolved || "（空）") + (d.resolvedFamily ? "  [" + d.resolvedFamily + "]" : ""));
        lines.push("连接来源：" + (d.remoteAddr || "") +
          "  可信代理=" + (d.remoteTrusted ? "是" : "否") +
          "  公网=" + (d.remotePublic ? "是" : "否"));
        lines.push("信任的头：" + (d.trustedHeaders || []).join(", "));
        lines.push("本次收到的头：");
        var h = d.clientHeaders || {};
        var keys = Object.keys(h);
        if (!keys.length) lines.push("  （一个都没有）");
        keys.forEach(function (k) { lines.push("  " + k + ": " + h[k]); });
        lines.push("");
        lines.push("若「解析结果」不是你的真实地址，说明 CDN 回源没带对应的头，");
        lines.push("或者源站前面还有一层反代把 X-Real-IP / X-Forwarded-For 覆盖了。");
        out.textContent = lines.join("\n");
      }).catch(function (e) {
        out.textContent = "检测失败：" + e.message;
      });
    });
  }

  function probe(node) {
    var list = [];
    if (node.url) list.push({ label: "IPv4", url: node.url });
    if (node.url6) list.push({ label: "IPv6", url: node.url6 });
    if (!list.length) {
      toast("本机节点无需测试");
      return;
    }
    toast("正在测试 " + node.name + " …");
    probeURLs(list).then(function (res) {
      var bad = res.filter(function (r) { return !r.ok; });
      if (!bad.length) {
        toast(node.name + " 可用，延迟约 " + Math.round(res[0].ms) + " ms");
      } else {
        toast(node.name + "：" + bad.map(function (r) { return r.label + " 不可用"; }).join("，"), true);
      }
    });
  }

  function removeNode(node) {
    if (!confirm("确定删除节点「" + node.name + "」？")) return;
    api("/api/admin/nodes/" + encodeURIComponent(node.id), { method: "DELETE" }).then(function () {
      toast("节点已删除");
      loadNodes();
      loadOverview();
    }).catch(function (err) { toast(err.message, true); });
  }

  /* ---------- 站点设置 ---------- */

  // 评级门槛默认值，与后端 defaultRating() / 前台 RATING_DEF 保持一致
  var RATING_DEF = {
    jitterWeight: 2,
    web: [40, 80, 150, 300],
    game: [30, 60, 100, 180],
    video: [50, 100, 180, 320],
    call: [40, 80, 140, 240],
    loss: [0.1, 0.5, 1.5, 4]
  };
  var RATE_KEYS = ["Web", "Game", "Video", "Call", "Loss"];

  function fillRating(rt) {
    rt = rt || {};
    if ($("setRateJitter")) {
      $("setRateJitter").value = rt.jitterWeight != null ? rt.jitterWeight : RATING_DEF.jitterWeight;
    }
    RATE_KEYS.forEach(function (k) {
      var def = RATING_DEF[k.toLowerCase()];
      var arr = rt[k.toLowerCase()] || def;
      for (var i = 0; i < 4; i++) {
        var el = $("setRate" + k + i);
        if (el) el.value = arr[i] != null ? arr[i] : def[i];
      }
    });
  }

  function readRating() {
    var out = { jitterWeight: parseFloat($("setRateJitter") ? $("setRateJitter").value : "") };
    if (!isFinite(out.jitterWeight) || out.jitterWeight < 0) out.jitterWeight = RATING_DEF.jitterWeight;
    RATE_KEYS.forEach(function (k) {
      var arr = [];
      for (var i = 0; i < 4; i++) {
        var el = $("setRate" + k + i);
        var v = el ? parseFloat(el.value) : NaN;
        arr.push(isFinite(v) && v >= 0 ? v : RATING_DEF[k.toLowerCase()][i]);
      }
      out[k.toLowerCase()] = arr;
    });
    return out;
  }

  function fillSettings(s) {
    if (!s) return;
    $("setSiteName").value = s.siteName || "";
    if ($("setTitleSuffix")) $("setTitleSuffix").value = s.titleSuffix || "";
    $("setFooter").value = s.footerNote || "";
    $("setConns").value = s.maxConnections || 6;
    $("setDl").value = s.downloadSeconds || 15;
    $("setUl").value = s.uploadSeconds || 15;
    $("setGauge").value = s.gaugeMax || 1000;
    fillRating(s.rating || s.ratingCfg);
    $("setAllowSelect").checked = !!s.allowNodeSelect;
    $("setAutoPick").checked = s.autoSelectNode !== false;
    $("setShowIP").checked = !!s.showClientIP;
    $("setShowLive").checked = !s.hideLive;
    $("setCollect").checked = !!s.collectResults;
    if ($("setProbeV4")) $("setProbeV4").value = s.probeV4 || "";
    if ($("setProbeV6")) $("setProbeV6").value = s.probeV6 || "";
    if ($("setPathLangZh")) $("setPathLangZh").value = s.pathLangZh || "";
    if ($("setPathLangEn")) $("setPathLangEn").value = s.pathLangEn || "";
    if ($("setPathV4")) $("setPathV4").value = s.pathV4 || "";
    if ($("setPathV6")) $("setPathV6").value = s.pathV6 || "";
    if ($("setPathResult")) $("setPathResult").value = s.pathResult || "";
    if ($("setAdminPath")) {
      $("setAdminPath").value = s.adminPath || "";
      $("adminPathNow").textContent = "/" + (s.adminPath || "");
    }
    $("headSite").textContent = s.siteName || "SpeedTest";
    if ($("setHideIcon")) $("setHideIcon").checked = !!s.hideLogoIcon;
    var prev = $("iconPreview");
    if (prev) prev.src = s.iconUrl || "/favicon.svg";
    fillDefaultNodeSelect();
  }

  $("settingsForm").addEventListener("submit", function (e) {
    e.preventDefault();
    var body = {
      siteName: $("setSiteName").value.trim() || "SpeedTest",
      titleSuffix: $("setTitleSuffix") ? $("setTitleSuffix").value.trim() : "",
      tagline: state.settings ? state.settings.tagline : "",
      defaultNode: $("setDefaultNode").value,
      defaultNode6: $("setDefaultNode6").value,
      footerNote: $("setFooter").value.trim(),
      maxConnections: parseInt($("setConns").value, 10) || 6,
      downloadSeconds: parseInt($("setDl").value, 10) || 15,
      uploadSeconds: parseInt($("setUl").value, 10) || 15,
      gaugeMax: parseInt($("setGauge").value, 10) || 1000,
      rating: readRating(),
      allowNodeSelect: $("setAllowSelect").checked,
      hideLogoIcon: $("setHideIcon") ? $("setHideIcon").checked : false,
      autoSelectNode: $("setAutoPick").checked,
      showClientIP: $("setShowIP").checked,
      hideLive: !$("setShowLive").checked,
      collectResults: $("setCollect").checked,
      probeV4: $("setProbeV4") ? $("setProbeV4").value.trim() : "",
      probeV6: $("setProbeV6") ? $("setProbeV6").value.trim() : "",
      pathLangZh: $("setPathLangZh") ? $("setPathLangZh").value.trim() : "",
      pathLangEn: $("setPathLangEn") ? $("setPathLangEn").value.trim() : "",
      pathV4: $("setPathV4") ? $("setPathV4").value.trim() : "",
      pathV6: $("setPathV6") ? $("setPathV6").value.trim() : "",
      pathResult: $("setPathResult") ? $("setPathResult").value.trim() : "",
      adminPath: $("setAdminPath") ? $("setAdminPath").value.trim() : ""
    };
    api("/api/admin/settings", { method: "PUT", body: body }).then(function (d) {
      // 改了隐藏路径就立刻跳到新地址（旧地址马上失效），登录 Cookie 仍然有效
      var next = (d && d.adminPath) || body.adminPath;
      if (next && ("/" + next) !== location.pathname.replace(/\/+$/, "")) {
        toast("后台地址已改为 /" + next);
        setTimeout(function () { location.href = "/" + next; }, 800);
        return null;
      }
      toast("设置已保存");
      return api("/api/admin/overview");
    }).then(function (data) {
      if (!data) return;
      state.settings = data.settings;
      fillSettings(data.settings);
    }).catch(function (err) { toast(err.message, true); });
  });

  /* ---------- 站点图标上传 ---------- */
  function refreshIconPreview(url) {
    var prev = $("iconPreview");
    if (prev) prev.src = (url || "/favicon.svg") + (url ? "" : ("?t=" + Date.now()));
    if (state.settings) {
      state.settings.iconUrl = url || "";
      // 同步后台顶部品牌图标
      // 后台上传/重置后立刻同步品牌区：有图标就换成 img，没有就还原默认圆环
      var brandImg = document.querySelector(".a-brand img");
      var brandSvg = document.querySelector(".a-brand svg");
      if (url) {
        if (!brandImg) {
          brandImg = document.createElement("img");
          brandImg.className = "a-brand-icon";
          brandImg.alt = "";
          var host = document.querySelector(".a-brand");
          if (host) host.insertBefore(brandImg, host.firstChild);
        }
        brandImg.src = url;
        if (brandSvg) brandSvg.style.display = "none";
      } else {
        if (brandImg) brandImg.remove();
        if (brandSvg) brandSvg.style.display = "";
      }
    }
  }

  if ($("uploadIconBtn") && $("setIconFile")) {
    $("uploadIconBtn").addEventListener("click", function () { $("setIconFile").click(); });
    $("setIconFile").addEventListener("change", function () {
      var f = $("setIconFile").files && $("setIconFile").files[0];
      if (!f) return;
      var fd = new FormData();
      fd.append("icon", f);
      toast("正在上传图标…");
      fetch("/api/admin/icon", { method: "POST", credentials: "same-origin", body: fd })
        .then(function (res) {
          return res.json().catch(function () { return {}; }).then(function (d) {
            if (!res.ok || d.ok === false) throw new Error(d.error || "上传失败");
            refreshIconPreview(d.iconUrl);
            toast("图标已更新");
          });
        })
        .catch(function (err) { toast(err.message, true); });
      $("setIconFile").value = "";
    });
  }
  if ($("resetIconBtn")) {
    $("resetIconBtn").addEventListener("click", function () {
      api("/api/admin/icon", { method: "DELETE" }).then(function (d) {
        refreshIconPreview("");
        toast("已恢复默认图标");
      }).catch(function (err) { toast(err.message, true); });
    });
  }

  /* ---------- 账号 ---------- */

  $("accountForm").addEventListener("submit", function (e) {
    e.preventDefault();
    $("accErr").textContent = "";
    var user = $("accUser").value.trim();
    var oldPass = $("accOld").value;
    var n1 = $("accNew").value;
    var n2 = $("accNew2").value;
    if (!oldPass) { $("accErr").textContent = "请输入当前密码"; return; }
    if (n1.length < 6) { $("accErr").textContent = "新密码至少 6 位"; return; }
    if (n1 !== n2) { $("accErr").textContent = "两次输入的新密码不一致"; return; }
    api("/api/admin/password", {
      method: "POST",
      body: { username: user, oldPassword: oldPass, newPassword: n1 }
    }).then(function () {
      $("accOld").value = "";
      $("accNew").value = "";
      $("accNew2").value = "";
      toast("账号密码已更新");
      $("pwdWarn").hidden = true;
      loadOverview();
    }).catch(function (err) {
      $("accErr").textContent = err.message;
    });
  });

  /* ---------- 启动 ---------- */

  api("/api/admin/me").then(function (data) {
    state.username = data.username;
    showApp();
  }).catch(function () {
    showLogin();
  });
})();
