(function () {
  "use strict";

  var $ = function (id) { return document.getElementById(id); };

  var state = { nodes: [], settings: null, results: [], username: "" };

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

  function showApp() {
    $("loginView").hidden = true;
    $("appView").hidden = false;
    $("logoutBtn").hidden = false;
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
      // 一个节点可以同时有 IPv4 / IPv6 两个接入地址
      var addr = [];
      if (n.url) addr.push("<div class='mono'>IPv4 " + esc(n.url) + "</div>");
      if (n.url6) addr.push("<div class='mono'>IPv6 " + esc(n.url6) + "</div>");
      if (!addr.length) addr.push("<div class='mono'>本机地址</div>");
      return "<tr>" +
        "<td><div>" + esc(n.name) + "</div><div class='mono'>" + tags.join(" ") + "</div></td>" +
        "<td>" + esc(n.location || "-") + "</td>" +
        "<td>" + esc(n.sponsor || "-") + "</td>" +
        "<td>" + addr.join("") + "</td>" +
        "<td><div class='row-actions'>" +
        "<button class='a-btn small' data-act='probe' data-id='" + esc(n.id) + "'>测试</button>" +
        "<button class='a-btn small' data-act='edit' data-id='" + esc(n.id) + "'>编辑</button>" +
        (n.local ? "" : "<button class='a-btn small danger' data-act='del' data-id='" + esc(n.id) + "'>删除</button>") +
        "</div></td></tr>";
    }).join("");
    box.innerHTML = "<table class='tbl'><thead><tr>" +
      "<th>节点</th><th>地区</th><th>运营方</th><th>地址</th><th style='text-align:right'>操作</th>" +
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
      });
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

  function fillSettings(s) {
    if (!s) return;
    $("setSiteName").value = s.siteName || "";
    if ($("setTitleSuffix")) $("setTitleSuffix").value = s.titleSuffix || "";
    $("setFooter").value = s.footerNote || "";
    $("setConns").value = s.maxConnections || 6;
    $("setDl").value = s.downloadSeconds || 15;
    $("setUl").value = s.uploadSeconds || 15;
    $("setGauge").value = s.gaugeMax || 1000;
    $("setAllowSelect").checked = !!s.allowNodeSelect;
    $("setAutoPick").checked = s.autoSelectNode !== false;
    $("setShowIP").checked = !!s.showClientIP;
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
      allowNodeSelect: $("setAllowSelect").checked,
      autoSelectNode: $("setAutoPick").checked,
      showClientIP: $("setShowIP").checked,
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
      var brand = document.querySelector(".a-brand img");
      if (brand) brand.src = prev ? prev.src : "/favicon.svg";
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
