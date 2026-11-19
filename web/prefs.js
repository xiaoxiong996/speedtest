/* 展示偏好：时间制式 / 日期格式 / 距离单位 / 速度单位
 *
 * 默认值：
 *   时间 12 小时制 · 日期 MM/DD/YYYY · 距离英里 · 速度 Mbps
 * 存在 localStorage 里，主页与报告页的「设置」面板改写，两处读同一份配置，
 * 所以改完立刻在报告页的测速时间、速度数字、节点距离上生效。
 *
 * 用法：
 *   PREFS.get("time")            "12" | "24"
 *   PREFS.set("speed", "kbps")   立即持久化（调用方负责重绘界面）
 *   PREFS.fmtTime(unix, true)    "11/18/2026 1:24:05 PM"
 *   PREFS.speedText(23.456)      "23.46" 或 Kbps 下的 "23456"
 *   PREFS.speedUnit()            "Mbps" | "Kbps"
 *   PREFS.distText(20)           距离（公里）→ "12.4 mi" 或 "20 km"
 */
(function (w) {
  "use strict";

  // 键名带版本号：老访客以前点过的 24 小时 / 公里等旧选择不再生效，
  // 直接落到新的出厂默认值上（不改键名的话，改默认值对老访客是无效的）。
  var KEY = "st_prefs2";
  var DEF = { time: "12", date: "mdy", dist: "mi", speed: "mbps" };
  var KM_PER_MILE = 1.609344;
  var cache = null;

  function load() {
    var out = {};
    for (var k in DEF) {
      if (Object.prototype.hasOwnProperty.call(DEF, k)) out[k] = DEF[k];
    }
    var raw = null;
    try { raw = localStorage.getItem(KEY); } catch (e) {}
    if (raw) {
      try {
        var o = JSON.parse(raw);
        for (var key in out) {
          if (o && typeof o[key] === "string" && o[key]) out[key] = o[key];
        }
      } catch (e) {}
    }
    return out;
  }

  function all() {
    if (!cache) cache = load();
    return cache;
  }

  function get(k) {
    var v = all()[k];
    return v == null ? (DEF[k] || "") : v;
  }

  function set(k, v) {
    if (v == null || v === "") return;
    all()[k] = v;
    try { localStorage.setItem(KEY, JSON.stringify(all())); } catch (e) {}
  }

  function pad(n) { return n < 10 ? "0" + n : "" + n; }

  function fmtDate(d) {
    var y = d.getFullYear(), mo = pad(d.getMonth() + 1), da = pad(d.getDate());
    switch (get("date")) {
      case "mdy": return mo + "/" + da + "/" + y;
      case "dmy": return da + "/" + mo + "/" + y;
      case "ymd-dash": return y + "-" + mo + "-" + da;
      default: return y + "/" + mo + "/" + da;
    }
  }

  // withDate=true 时返回「日期 + 时间」，否则只返回时分秒
  function fmtTime(unix, withDate) {
    var d = new Date(unix * 1000);
    var h = d.getHours(), m = d.getMinutes(), s = d.getSeconds();
    var time;
    if (get("time") === "12") {
      var h12 = h % 12;
      if (h12 === 0) h12 = 12;
      time = h12 + ":" + pad(m) + ":" + pad(s) + " " + (h >= 12 ? "PM" : "AM");
    } else {
      time = pad(h) + ":" + pad(m) + ":" + pad(s);
    }
    return withDate ? fmtDate(d) + " " + time : time;
  }

  function speedUnit() {
    return get("speed") === "kbps" ? "Kbps" : "Mbps";
  }

  // Mbps 保留两位小数；Kbps 数值大，取整更清爽
  function speedText(v) {
    if (!isFinite(v) || v <= 0) return get("speed") === "kbps" ? "0" : "0.00";
    if (get("speed") === "kbps") {
      var k = v * 1000;
      return k >= 100 ? k.toFixed(0) : k.toFixed(1);
    }
    return v.toFixed(2);
  }

  function distUnit() {
    return get("dist") === "mi" ? "mi" : "km";
  }

  // 传入公里数，按偏好换算成英里 / 公里
  function distText(km) {
    km = Number(km);
    if (!isFinite(km) || km < 0) return "";
    var v = distUnit() === "mi" ? km / KM_PER_MILE : km;
    var txt = v >= 100 ? String(Math.round(v)) : v.toFixed(1);
    return txt + " " + distUnit();
  }

  w.PREFS = {
    get: get,
    set: set,
    fmtTime: fmtTime,
    fmtDate: fmtDate,
    speedUnit: speedUnit,
    speedText: speedText,
    distUnit: distUnit,
    distText: distText,
    // 固定的示例值：设置面板里用它实时展示四项偏好的效果
    sampleText: function () {
      var d = new Date(2026, 10, 18, 13, 24, 5);
      return fmtDate(d) + " " + fmtTime(Math.floor(d.getTime() / 1000), false) +
        " · " + speedText(54.28) + " " + speedUnit();
    },
    defaults: DEF
  };
})(window);
