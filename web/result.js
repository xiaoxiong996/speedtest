(function () {
  "use strict";

  var $ = function (id) { return document.getElementById(id); };
  var t = function (key, vars) { return window.I18N ? window.I18N.t(key, vars) : key; };

  var segments = location.pathname.replace(/\/+$/, "").split("/");
  var resultID = segments[segments.length - 1] || "";

  // 分享链接沿用当前地址（含语言 /ipv4、/ipv6 协议前缀），旧格式链接也能正常打开
  function pageURL() {
    return location.origin + location.pathname.replace(/\/+$/, "");
  }

  function toast(msg) {
    var t = $("toast");
    t.textContent = msg;
    t.className = "toast show";
    clearTimeout(t._t);
    t._t = setTimeout(function () { t.className = "toast"; }, 2200);
  }

  // 速度按「设置」里的偏好显示：Mbps（两位小数）或 Kbps（换算后取整）
  function fmtSpeed(v) {
    if (window.PREFS) return PREFS.speedText(v);
    if (!isFinite(v) || v <= 0) return "0.00";
    return v.toFixed(2);
  }

  function fmtPing(v) {
    if (!isFinite(v) || v <= 0) return "--";
    return v < 10 ? v.toFixed(1) : v.toFixed(0);
  }

  // 流量按 1024 进制显示，最多保留 2 位小数
  function fmtBytes(n) {
    n = Number(n) || 0;
    if (n <= 0) return "0 MB";
    var units = ["B", "KB", "MB", "GB", "TB"];
    var i = 0;
    while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
    var txt = n >= 100 || i === 0 ? Math.round(n) : n.toFixed(n >= 10 ? 1 : 2);
    return txt + " " + units[i];
  }

  // 测速者凭证：测速成功时由首页写进 localStorage，别人拿到链接也没有
  function ownerToken() {
    try {
      var map = JSON.parse(localStorage.getItem("st_rc") || "{}") || {};
      return map[resultID] || "";
    } catch (e) {
      return "";
    }
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

  // 测速时间按「设置」里的时间制式 / 日期格式显示
  function fmtTime(unix) {
    if (window.PREFS) return PREFS.fmtTime(unix, true);
    var d = new Date(unix * 1000);
    var pad = function (n) { return n < 10 ? "0" + n : "" + n; };
    return d.getFullYear() + "/" + pad(d.getMonth() + 1) + "/" + pad(d.getDate()) +
      " " + pad(d.getHours()) + ":" + pad(d.getMinutes());
  }

  /* ---------------- 体验评级 ---------------- */

  // 与首页同一套门槛：服务端随报告下发 ratingCfg，缺字段时回落到默认值。
  var RATING_DEF = {
    jitterWeight: 2,
    web: [40, 80, 150, 300],
    game: [30, 60, 100, 180],
    video: [50, 100, 180, 320],
    call: [40, 80, 140, 240],
    loss: [0.1, 0.5, 1.5, 4]
  };
  var RATING = RATING_DEF;

  function ratingSteps(v, def) {
    if (!v || v.length !== def.length) return def;
    for (var i = 0; i < v.length; i++) if (!isFinite(v[i]) || v[i] < 0) return def;
    return v;
  }

  function setRatingCfg(c) {
    c = c || {};
    RATING = {
      jitterWeight: isFinite(c.jitterWeight) && c.jitterWeight >= 0 ? c.jitterWeight : RATING_DEF.jitterWeight,
      web: ratingSteps(c.web, RATING_DEF.web),
      game: ratingSteps(c.game, RATING_DEF.game),
      video: ratingSteps(c.video, RATING_DEF.video),
      call: ratingSteps(c.call, RATING_DEF.call),
      loss: ratingSteps(c.loss, RATING_DEF.loss)
    };
  }

  // 有效延迟 = 延迟 + 抖动 × 权重，再用 4 个阈值切 5 档（越小越好）
  function levelByLatency(ms, jitter, steps) {
    if (!isFinite(ms) || ms <= 0) return 0;
    var eff = ms + Math.max(0, isFinite(jitter) ? jitter : 0) * RATING.jitterWeight;
    for (var i = 0; i < steps.length; i++) if (eff <= steps[i]) return 5 - i;
    return 1;
  }

  function levelByLoss(pct, steps) {
    var v = isFinite(pct) ? pct : 0;
    for (var i = 0; i < steps.length; i++) if (v <= steps[i]) return 5 - i;
    return 1;
  }

  function buildExp() {
    var boxes = document.querySelectorAll("#expRow .exp");
    for (var i = 0; i < boxes.length; i++) {
      var dots = boxes[i].querySelector(".exp-dots");
      if (!dots || dots.children.length) continue;
      for (var j = 0; j < 5; j++) dots.appendChild(document.createElement("i"));
    }
  }

  function renderExp(ping, jitter) {
    var lv = {
      web: levelByLatency(ping, jitter, RATING.web),
      game: levelByLatency(ping, jitter, RATING.game),
      video: levelByLatency(ping, jitter, RATING.video),
      call: levelByLatency(ping, jitter, RATING.call)
    };
    var boxes = document.querySelectorAll("#expRow .exp");
    for (var i = 0; i < boxes.length; i++) {
      var dots = boxes[i].querySelectorAll(".exp-dots i");
      var n = lv[boxes[i].getAttribute("data-exp")] || 0;
      for (var j = 0; j < dots.length; j++) dots[j].className = j < n ? "on" : "";
    }
  }

  // 分享按钮的链接跟着当前语言/协议路径走，切了语言也会刷新
  function syncShareLinks() {
    var u = encodeURIComponent(pageURL());
    var text = encodeURIComponent(t("reportTitle", { id: resultID, site: document.title }));
    if ($("shareX")) $("shareX").href = "https://twitter.com/intent/tweet?url=" + u + "&text=" + text;
    if ($("shareFB")) $("shareFB").href = "https://www.facebook.com/sharer/sharer.php?u=" + u;
  }

  function setupShare() {
    syncShareLinks();
    var copy = function () {
      var url = pageURL();
      var done = function () { toast(t("linkCopied")); };
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(url).then(done).catch(function () { toast(url); });
      } else {
        toast(url);
      }
    };
    $("copyBtn").addEventListener("click", copy);
    // 「更多」：能唤起系统分享面板就唤起，否则退回复制链接
    var more = $("shareMore");
    if (more) {
      more.addEventListener("click", function () {
        if (navigator.share) {
          navigator.share({ title: document.title, url: pageURL() }).catch(function () {});
        } else {
          copy();
        }
      });
    }
  }

  /* ---------------- 设置面板 ---------------- */

  // 把当前偏好回填到面板的高亮状态，并刷新底部的示例行
  function readPrefsIntoUI() {
    if (!window.PREFS) return;
    var groups = document.querySelectorAll("#setPanel .set-opts");
    for (var i = 0; i < groups.length; i++) {
      var val = PREFS.get(groups[i].getAttribute("data-pref"));
      var opts = groups[i].querySelectorAll(".set-opt");
      for (var j = 0; j < opts.length; j++) {
        opts[j].classList.toggle("active", opts[j].getAttribute("data-val") === val);
      }
    }
    var sel = $("setDateFmt");
    if (sel) sel.value = PREFS.get("date");
    if ($("setPreview")) $("setPreview").textContent = t("spPreview") + PREFS.sampleText();
  }

  function toggleSetPanel(force) {
    var panel = $("setPanel"), btn = $("setBtn");
    if (!panel || !btn) return;
    var open = typeof force === "boolean" ? force : panel.hidden;
    panel.hidden = !open;
    btn.setAttribute("aria-expanded", open ? "true" : "false");
    if (open) readPrefsIntoUI();
  }

  // 改了偏好就照新规则重算本页的时间、速度数字与单位
  function applyPrefs() {
    readPrefsIntoUI();
    if (lastData) render(lastData);
  }

  /* ---------------- 顶栏（品牌 / 菜单 / 主题切换） ---------------- */

  function setupHeader() {
    var tbtn = $("themeBtn");
    if (tbtn) {
      tbtn.addEventListener("click", function () {
        var next = document.documentElement.getAttribute("data-theme") === "light" ? "dark" : "light";
        document.documentElement.setAttribute("data-theme", next);
        try { localStorage.setItem("st_theme", next); } catch (e) {}
      });
    }

    var mbtn = $("hdrMenuBtn"), mpanel = $("hdrMenuPanel");
    if (mbtn && mpanel) {
      mbtn.addEventListener("click", function (e) {
        e.stopPropagation();
        var open = mpanel.hidden;
        mpanel.hidden = !open;
        mbtn.setAttribute("aria-expanded", open ? "true" : "false");
      });
      document.addEventListener("click", function () {
        if (!mpanel.hidden) {
          mpanel.hidden = true;
          mbtn.setAttribute("aria-expanded", "false");
        }
      });
    }

    var hs = $("hdrSettings");
    if (hs) hs.addEventListener("click", function () {
      if (mpanel) mpanel.hidden = true;
      if ($("setBtn")) $("setBtn").click();
    });
  }

  function setupSettings() {
    var btn = $("setBtn"), panel = $("setPanel");
    if (!btn || !panel) return;
    btn.addEventListener("click", function (e) {
      e.stopPropagation();
      toggleSetPanel();
    });
    panel.addEventListener("click", function (e) {
      var opt = e.target.closest ? e.target.closest(".set-opt") : null;
      if (!opt) return;
      var group = opt.closest(".set-opts");
      if (!group || !window.PREFS) return;
      PREFS.set(group.getAttribute("data-pref"), opt.getAttribute("data-val"));
      applyPrefs();
    });
    panel.addEventListener("change", function (e) {
      var sel = e.target;
      if (!sel || !sel.getAttribute || !sel.getAttribute("data-pref") || !window.PREFS) return;
      PREFS.set(sel.getAttribute("data-pref"), sel.value);
      applyPrefs();
    });
    document.addEventListener("click", function (e) {
      if (panel.hidden) return;
      if (panel.contains(e.target) || btn.contains(e.target)) return;
      toggleSetPanel(false);
    });
    readPrefsIntoUI();
  }

  function buildScale() {
    var box = $("scale");
    for (var i = 0; i <= 10; i++) {
      (function (n) {
        var b = document.createElement("button");
        b.type = "button";
        b.textContent = String(n);
        b.addEventListener("click", function () {
          Array.prototype.forEach.call(box.children, function (c) { c.classList.remove("on"); });
          b.classList.add("on");
          fetch("/api/result/" + encodeURIComponent(resultID) + "/rate", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ score: n })
          }).then(function () {
            var q = document.createElement("div");
            q.className = "thanks";
            q.textContent = t("thanks", { n: n });
            box.parentNode.insertBefore(q, box);
          }).catch(function () {});
        });
        box.appendChild(b);
      })(i);
    }
  }

  /* ---------------- 逐秒曲线 ---------------- */

  var TL_W = 640, TL_H = 208;
  var TL_L = 44, TL_R = 16, TL_T = 22, TL_B = 26;
  var tlChart = null;

  // 轴上限取一个好看的整数档位，曲线顶部不会贴着边框
  function niceTop(v) {
    if (!isFinite(v) || v <= 0) return 10;
    var p = Math.pow(10, Math.floor(Math.log(v) / Math.LN10));
    var n = v / p;
    var f = n <= 1 ? 1 : n <= 1.5 ? 1.5 : n <= 2 ? 2 : n <= 2.5 ? 2.5 : n <= 3 ? 3
      : n <= 4 ? 4 : n <= 5 ? 5 : n <= 6 ? 6 : n <= 8 ? 8 : 10;
    return f * p;
  }

  function shortNum(v) {
    if (!isFinite(v)) return "0";
    if (v >= 1000) return String(Math.round(v / 100) / 10) + "k";
    if (v >= 10) return String(Math.round(v));
    if (v >= 1) return String(Math.round(v * 10) / 10);
    return String(Math.round(v * 100) / 100);
  }

  // 用二次贝塞尔平滑折线：控制点落在数据点上，不会像三次曲线那样冲高或跌到负值
  function tlPath(list) {
    if (!list.length) return "";
    if (list.length === 1) return "M" + list[0].x.toFixed(1) + " " + list[0].y.toFixed(1);
    var d = "M" + list[0].x.toFixed(1) + " " + list[0].y.toFixed(1);
    for (var i = 1; i < list.length; i++) {
      var prev = list[i - 1], cur = list[i];
      d += " Q" + cur.x.toFixed(1) + " " + cur.y.toFixed(1) + " " +
        ((prev.x + cur.x) / 2).toFixed(1) + " " + ((prev.y + cur.y) / 2).toFixed(1);
    }
    var last = list[list.length - 1];
    d += " L" + last.x.toFixed(1) + " " + last.y.toFixed(1);
    return d;
  }

  function tlArea(list, base) {
    if (list.length < 2) return "";
    return tlPath(list) +
      " L" + list[list.length - 1].x.toFixed(1) + " " + base +
      " L" + list[0].x.toFixed(1) + " " + base + " Z";
  }

  function tlSpeedText(v) {
    if (window.PREFS) return PREFS.speedText(isFinite(v) ? v : 0);
    return (isFinite(v) ? v : 0).toFixed(2);
  }

  function tlUnit() {
    return window.PREFS ? PREFS.speedUnit() : "Mbps";
  }

  function buildChart(points, downAvg, upAvg) {
    var box = $("tlBox"), plot = $("tlPlot");
    if (!box || !plot) return;
    var pts = (points || []).filter(function (p) { return p && isFinite(p.t) && p.t > 0; })
      .map(function (p) {
        // omitempty 会把 0 省掉，缺字段的按 0 处理，否则路径里会出现 NaN 整条画不出来
        return { t: Number(p.t) || 0, d: Number(p.d) || 0, u: Number(p.u) || 0, p: Number(p.p) || 0 };
      })
      .sort(function (a, b) { return a.t - b.t; });
    tlChart = null;

    function empty() {
      plot.innerHTML = '<div class="tl-empty">' + t("tlNoData") + "</div>";
      box.hidden = false;
    }
    if (pts.length < 2) { empty(); return; }

    // 延迟阶段在最前面（约 5 秒），曲线不再画它；下行、上行各测 15 秒，
    // 各自去掉阶段起点后叠到同一根 0–15s 轴上，两条曲线共用一根 Mbps 纵轴。
    var pingEnd = 0, dlEnd = 0;
    pts.forEach(function (p) {
      if (p.p > 0 && p.t > pingEnd) pingEnd = p.t;
      if (p.d > 0 && p.t > dlEnd) dlEnd = p.t;
    });
    if (!dlEnd) dlEnd = pingEnd;
    var dRaw = pts.filter(function (p) { return p.t > pingEnd && p.t <= dlEnd; });
    var uRaw = pts.filter(function (p) { return p.t > dlEnd; });
    if (!dRaw.length && !uRaw.length) { empty(); return; }

    var dBase = dRaw.length ? dRaw[0].t - 1 : 0;
    var uBase = uRaw.length ? uRaw[0].t - 1 : 0;
    var total = Math.max(
      dRaw.length ? dRaw[dRaw.length - 1].t - dBase : 0,
      uRaw.length ? uRaw[uRaw.length - 1].t - uBase : 0,
      1
    );

    // 同一秒把下行 / 上行并到一条记录里，拖动时一次显示两个数值
    var bySec = {};
    dRaw.forEach(function (p) {
      var s = Math.round(p.t - dBase);
      if (!bySec[s]) bySec[s] = { s: s, d: 0, u: 0, hd: false, hu: false };
      bySec[s].d = p.d;
      bySec[s].hd = true;
    });
    uRaw.forEach(function (p) {
      var s = Math.round(p.t - uBase);
      if (!bySec[s]) bySec[s] = { s: s, d: 0, u: 0, hd: false, hu: false };
      bySec[s].u = p.u;
      bySec[s].hu = true;
    });
    var merged = Object.keys(bySec).map(Number)
      .filter(function (s) { return s > 0 && s <= total; })
      .sort(function (a, b) { return a - b; })
      .map(function (s) { return bySec[s]; });
    if (!merged.length) { empty(); return; }

    var x0 = TL_L, x1 = TL_W - TL_R, y0 = TL_T, y1 = TL_H - TL_B;
    // 纵轴不跟单点峰值走：慢启动那一下偶尔会飙到平均值的三四倍，
    // 按峰值定量程会把整条曲线压成贴地的一条线，所以取 90 分位 + 报告值的较大者
    var speeds = merged.map(function (p) { return Math.max(p.d, p.u); })
      .filter(function (v) { return v > 0; }).sort(function (a, b) { return a - b; });
    var p90 = speeds.length ? speeds[Math.min(speeds.length - 1, Math.floor(speeds.length * 0.9))] : 0;
    var avgTop = Math.max(Number(downAvg) || 0, Number(upAvg) || 0);
    var topD = niceTop(Math.max(p90 * 1.25, avgTop * 1.2, 2));
    var px = function (s) { return x0 + (s / total) * (x1 - x0); };
    var py = function (v) { return y1 - Math.min(1, Math.max(0, v / topD)) * (y1 - y0); };
    var sD = dRaw.map(function (p) { return { x: px(p.t - dBase), y: py(p.d) }; });
    var sU = uRaw.map(function (p) { return { x: px(p.t - uBase), y: py(p.u) }; });

    var out = [];
    out.push('<svg viewBox="0 0 ' + TL_W + " " + TL_H + '" role="img" aria-label="' + t("tlTitle") + '">');
    out.push("<defs>");
    out.push('<linearGradient id="tlFillD" x1="0" y1="0" x2="0" y2="1">' +
      '<stop offset="0" stop-color="#00c9c0" stop-opacity=".3"/>' +
      '<stop offset="1" stop-color="#00c9c0" stop-opacity="0"/></linearGradient>');
    out.push('<linearGradient id="tlFillU" x1="0" y1="0" x2="0" y2="1">' +
      '<stop offset="0" stop-color="#b75cf0" stop-opacity=".28"/>' +
      '<stop offset="1" stop-color="#b75cf0" stop-opacity="0"/></linearGradient>');
    out.push("</defs>");

    // 横向网格 + 左侧速度刻度：上下行共用一根 Mbps 轴，不再有右侧 ms 轴
    [0, 0.5, 1].forEach(function (f) {
      var gy = y1 - f * (y1 - y0);
      out.push('<line class="tl-grid" x1="' + x0 + '" y1="' + gy.toFixed(1) + '" x2="' + x1 +
        '" y2="' + gy.toFixed(1) + '"/>');
      out.push('<text class="tl-axis" x="' + (x0 - 8) + '" y="' + (gy + 3.4).toFixed(1) +
        '" text-anchor="end">' + shortNum(topD * f) + "</text>");
    });
    out.push('<text class="tl-axis" x="' + (x0 - 8) + '" y="' + (y0 - 8) +
      '" text-anchor="end">' + tlUnit() + "</text>");

    // 时间轴：0 → 15s（时长随后台设置变化时自动跟着变）
    var step = total > 30 ? 10 : total > 12 ? 5 : total > 6 ? 2 : 1;
    var ticks = [];
    for (var tt = 0; tt <= total + 0.01; tt += step) ticks.push(Math.round(tt));
    if (ticks[ticks.length - 1] !== Math.round(total)) ticks.push(Math.round(total));
    ticks.forEach(function (s) {
      var gx = px(s);
      out.push('<line class="tl-grid v" x1="' + gx.toFixed(1) + '" y1="' + y0 + '" x2="' +
        gx.toFixed(1) + '" y2="' + y1 + '"/>');
      out.push('<text class="tl-axis" x="' + gx.toFixed(1) + '" y="' + (y1 + 15) +
        '" text-anchor="middle">' + s + "s</text>");
    });

    out.push('<path d="' + tlArea(sU, y1) + '" fill="url(#tlFillU)"/>');
    out.push('<path d="' + tlArea(sD, y1) + '" fill="url(#tlFillD)"/>');
    out.push('<path class="tl-line u" d="' + tlPath(sU) + '"/>');
    out.push('<path class="tl-line d" d="' + tlPath(sD) + '"/>');

    // 拖动用的十字线与两个取值点
    out.push('<g id="tlCursor" hidden>');
    out.push('<line class="tl-cross" y1="' + y0 + '" y2="' + y1 + '"/>');
    out.push('<circle class="tl-cdot d" r="3.4" hidden/>');
    out.push('<circle class="tl-cdot u" r="3.4" hidden/>');
    out.push("</g>");
    out.push('<rect x="0" y="0" width="' + TL_W + '" height="' + TL_H + '" fill="transparent"/>');
    out.push("</svg>");

    box.hidden = false;
    plot.innerHTML = out.join("") + '<div class="tl-tip" id="tlTip" hidden></div>';

    tlChart = { pts: merged, total: total, px: px, py: py };
    setupChartDrag(plot);
  }

  function setupChartDrag(plot) {
    var svg = plot.querySelector("svg");
    var tip = $("tlTip");
    var cursor = $("tlCursor");
    if (!svg || !tip || !cursor || !tlChart) return;
    var cross = cursor.querySelector("line");
    var dots = {
      d: cursor.querySelector(".tl-cdot.d"),
      u: cursor.querySelector(".tl-cdot.u")
    };
    var dragging = false;

    function hide() {
      cursor.hidden = true;
      tip.hidden = true;
    }

    function mark(el, p, has, y) {
      if (!p || !has) { el.hidden = true; return; }
      el.hidden = false;
      el.setAttribute("cx", tlChart.px(p.s).toFixed(1));
      el.setAttribute("cy", y.toFixed(1));
    }

    function show(clientX) {
      var c = tlChart;
      if (!c) return;
      var rect = svg.getBoundingClientRect();
      if (!rect.width) return;
      var vx = ((clientX - rect.left) / rect.width) * TL_W;
      var want = ((vx - TL_L) / (TL_W - TL_L - TL_R)) * c.total;
      var best = c.pts[0], bd = Infinity;
      for (var i = 0; i < c.pts.length; i++) {
        var dd = Math.abs(c.pts[i].s - want);
        if (dd < bd) { bd = dd; best = c.pts[i]; }
      }
      var bx = c.px(best.s);
      cross.hidden = false;
      cross.setAttribute("x1", bx.toFixed(1));
      cross.setAttribute("x2", bx.toFixed(1));
      mark(dots.d, best, best.hd, c.py(best.d));
      mark(dots.u, best, best.hu, c.py(best.u));
      cursor.hidden = false;

      var rows = [
        { cls: "dl", name: t("download"), val: best.hd ? tlSpeedText(best.d) : "--", unit: tlUnit() },
        { cls: "ul", name: t("upload"), val: best.hu ? tlSpeedText(best.u) : "--", unit: tlUnit() }
      ].map(function (r) {
        return '<div class="tip-row"><i class="' + r.cls + '"></i>' +
          '<span>' + r.name + "</span>" +
          '<span class="n">' + r.val + '</span><span class="u">' + r.unit + "</span></div>";
      }).join("");

      tip.innerHTML = '<div class="tip-t">' + t("tlSecond", { n: Math.round(best.s) }) + "</div>" + rows;
      tip.hidden = false;
      tip.style.left = (bx / TL_W) * rect.width + "px";
      tip.style.top = "0px";
      tip.style.transform = bx > TL_W * 0.56
        ? "translateX(calc(-100% - 12px))"
        : "translateX(12px)";
    }

    svg.addEventListener("pointermove", function (e) { show(e.clientX); });
    svg.addEventListener("pointerdown", function (e) {
      dragging = true;
      try { svg.setPointerCapture(e.pointerId); } catch (err) {}
      show(e.clientX);
      e.preventDefault();
    });
    svg.addEventListener("pointerup", function (e) {
      dragging = false;
      try { svg.releasePointerCapture(e.pointerId); } catch (err) {}
    });
    svg.addEventListener("pointercancel", function () { dragging = false; hide(); });
    svg.addEventListener("pointerleave", function () { if (!dragging) hide(); });
  }

  var lastData = null;

  // 一份报告只亮一次自己的 IP：按报告编号记，而不是按 IP 记。
  // 按 IP 记会有个坑：同一网络连测两份报告，第二份第一次打开就看不到 IP 了。
  var ipSeen = { id: null, shown: false };
  var IP_SEEN_KEY = "stIpSeen";

  function seenIds() {
    try {
      var v = JSON.parse(localStorage.getItem(IP_SEEN_KEY) || "[]");
      return Object.prototype.toString.call(v) === "[object Array]" ? v : [];
    } catch (e) { return []; }
  }

  function ipAlreadyShown(ip, rid) {
    rid = rid || "";
    if (!rid) return false;                    // 没有编号就按「第一次」放行
    if (ipSeen.id === rid) return !ipSeen.shown;   // 同页重渲染（切语言/单位）沿用首次结论
    var list = seenIds();
    var seen = list.indexOf(rid) >= 0;
    if (!seen) {
      list.push(rid);
      if (list.length > 80) list = list.slice(-80);
      try { localStorage.setItem(IP_SEEN_KEY, JSON.stringify(list)); } catch (e) {}
      try { localStorage.removeItem("stIpShown"); } catch (e) {}   // 清掉老版本按 IP 记的键
    }
    ipSeen = { id: rid, shown: !seen };
    return seen;
  }

  function render(d) {
    lastData = d;
    document.body.classList.remove("res-loading");
    var r = d.result;
    document.title = t("reportTitle", { id: r.id, site: r.siteName || "SpeedTest" });
    $("rid").textContent = r.id;
    $("ridTime").textContent = fmtTime(r.time);
    $("down").textContent = fmtSpeed(r.download);
    $("up").textContent = fmtSpeed(r.upload);
    if (window.PREFS) {
      var unit = PREFS.speedUnit();
      if ($("downUnit")) $("downUnit").textContent = unit;
      if ($("upUnit")) $("upUnit").textContent = unit;
    }
    setRatingCfg(r.ratingCfg);
    $("ping").textContent = fmtPing(r.ping);
    $("jitter").textContent = fmtPing(r.jitter);
    // 带载延迟：老报告没有这两个字段，显示 -- 而不是 0
    $("downLat").textContent = r.downLat > 0 ? fmtPing(r.downLat) : "--";
    $("upLat").textContent = r.upLat > 0 ? fmtPing(r.upLat) : "--";
    $("loss").textContent = (r.loss || 0) + "%";
    $("connMode").textContent = r.connections > 1 ? t("multi") : t("single");
    document.documentElement.setAttribute("data-conn", r.connections > 1 ? "multi" : "single");
    renderExp(r.ping, r.jitter);

    // 本次测速消耗的流量：下载 + 上传
    var dbytes = Number(r.downloadBytes) || 0;
    var ubytes = Number(r.uploadBytes) || 0;
    if (dbytes + ubytes > 0) {
      $("traffic").textContent = fmtBytes(dbytes + ubytes) +
        "  ·  ↓ " + fmtBytes(dbytes) + "  /  ↑ " + fmtBytes(ubytes);
      $("trafficRow").hidden = false;
    } else {
      $("trafficRow").hidden = true;
    }

    // 报告里同时存了中英文运营商名，按当前界面语言选一个
    var isp = (window.I18N && window.I18N.ispName ? window.I18N.ispName(r.isp, r.ispEn) : (r.isp || r.ispEn)) || t("unknownIsp");
    $("isp").textContent = isp;
    // 报告链接可以公开分享：测速者自己看得到 IP，别人打开时后端不下发
    // 一个 IP 只展示一次：首次打开显示，之后（含刷新）改显示「已隐藏」
    var ipTxt = r.ip && !ipAlreadyShown(r.ip, r.id) ? ipLines(r.ip)
      : (r.ip ? t("hidden") : (r.restricted ? t("hiddenOwnerOnly") : t("hidden")));
    $("ip").textContent = ipTxt;

    // 测速走的 IP 协议（旧报告没有这个字段，整行隐藏）
    if (r.ipVersion === "v4" || r.ipVersion === "v6") {
      $("protoMode").textContent = r.ipVersion === "v6" ? "IPv6" : "IPv4";
      $("protoRow").hidden = false;
    } else {
      $("protoRow").hidden = true;
    }
    $("rateIsp").textContent = isp;

    var node = r.node || {};
    $("nodeName").textContent = (node.sponsor || "").trim() || node.name || t("serverFallback");
    $("nodeLoc").textContent = (node.location || "").trim();
    // 设置面板里的「服务器」一行，展示本次测速用的节点
    if ($("setServer")) $("setServer").textContent = $("nodeName").textContent || t("serverFallback");

    $("foot").textContent = t("reportFoot", { site: r.siteName || "SpeedTest", id: r.id });
    if ($("hdrSiteName") && r.siteName) $("hdrSiteName").textContent = r.siteName;

    // 逐秒曲线：老报告没有 timeline 字段，会显示一条「无数据」提示
    buildChart(r.timeline, r.download, r.upload);

    if (r.rating > 0) {
      var box = $("scale");
      Array.prototype.forEach.call(box.children, function (c, i) {
        c.classList.toggle("on", i === r.rating);
      });
    }
  }

  function fail(msg) {
    // 取不到报告就不要留半张空壳：隐藏整张卡片，给一句提示和返回按钮
    document.body.classList.remove("res-loading");
    document.body.classList.add("res-error");
    $("rid").textContent = resultID || "--";
    var box = $("resState");
    if (box) {
      $("resStateTitle").textContent = msg || t("notFound");
      box.hidden = false;
      if (window.I18N) window.I18N.apply();
    }
  }

  function initLang() {
    if (!window.I18N) return;
    window.I18N.apply();
    var label = $("langLabel");
    if (label) label.textContent = t("langName");
    var items = document.querySelectorAll("[data-lang]");
    for (var i = 0; i < items.length; i++) {
      items[i].classList.toggle("on", items[i].getAttribute("data-lang") === window.I18N.lang);
    }
    var btn = $("langBtn");
    var panel = $("langPanel");
    if (btn && panel) {
      btn.addEventListener("click", function (e) {
        e.stopPropagation();
        panel.hidden = !panel.hidden;
        btn.setAttribute("aria-expanded", panel.hidden ? "false" : "true");
      });
      panel.addEventListener("click", function (e) {
        var target = e.target.closest("[data-target-lang]") || e.target.closest("[data-lang]");
        if (!target) return;
        window.I18N.set(target.getAttribute("data-lang"));
        panel.hidden = true;
        btn.setAttribute("aria-expanded", "false");
      });
      document.addEventListener("click", function () {
        panel.hidden = true;
        btn.setAttribute("aria-expanded", "false");
      });
    }
  }

  initLang();
  setupHeader();
  buildExp();
  setupShare();
  buildScale();
  setupSettings();

  document.addEventListener("langchange", function () {
    if (lastData) render(lastData);
    readPrefsIntoUI();
    syncShareLinks();
  });

  if (!resultID) {
    fail(t("missingId"));
    return;
  }

  var headers = {};
  var tok = ownerToken();
  if (tok) headers["X-Result-Owner"] = tok;

  function load(tries) {
    return fetch("/api/result/" + encodeURIComponent(resultID), { cache: "no-store", headers: headers })
      .then(function (r) {
        if (!r.ok) throw new Error("not found");
        return r.json();
      })
      .then(function (d) {
        if (!d || !d.ok) throw new Error("not found");
        render(d);
      })
      .catch(function (e) {
        // 刷新时偶发的一次请求失败不算「报告不存在」，稍后重试一次
        if (tries > 0) return new Promise(function (res) { setTimeout(res, 600); }).then(function () { return load(tries - 1); });
        return Promise.reject(e);
      });
  }

  load(1).catch(function () { fail(t("notFound")); });
})();
