/* 轻量多语言：中文（简体） / English
 * 用法：
 *   I18N.t("download")              取当前语言的文案
 *   I18N.t("more", { n: 2 })        占位符替换
 *   I18N.set("en")                  切换语言并重绘 data-i18n 节点
 *   <span data-i18n="download">下载</span>
 *   <button data-i18n-aria="close">…
 */
(function (w) {
  "use strict";

  var DICT = {
    zh: {
      /* 顶栏 */
      menu: "菜单",
      results: "结果",
      settings: "设置",
      admin: "管理后台",
      downloadApp: "下载",
      signIn: "登录",
      langTitle: "语言",
      langName: "中文(简体)",
      languagesMenu: "语言菜单",
      toggleTheme: "切换主题",

      /* 指标 */
      download: "下载",
      upload: "上传",
      latency: "延迟",
      pingTip: "延迟",
      jitterTip: "抖动",
      downLatTip: "下载延迟",
      upLatTip: "上传延迟",
      lossTip: "丢包",

      /* 结果条 */
      resultId: "结果 ID",
      copyLink: "复制链接",
      viewReport: "查看报告",

      /* 访客网络 */
      yourNetwork: "你的网络",
      unknownIsp: "未知运营商",
      detecting: "正在获取…",
      offline: "无法连接服务端",
      noPublicIp: "未获取到公网地址",
      hidden: "已隐藏",
      hiddenAdminOnly: "已隐藏（仅管理员可见）",
      hiddenOwnerOnly: "已隐藏（仅测速者可见）",
      trafficUsed: "消耗流量",

      /* 节点 */
      finding: "正在寻找最优节点…",
      noServer: "暂无可用节点",
      serverFallback: "测速节点",
      thisServer: "本机",
      more: "{n} 更多",
      change: "更换",
      toggleConnections: "切换连接方式",
      selectServer: "选择测速节点",
      noServersInList: "暂无可用的测速节点",
      unavailable: "不可用",

      /* 连接方式 */
      connections: "连接",
      multi: "多重",
      single: "单一",

      /* 协议 */
      protocol: "协议",
      ipv4: "IPv4",
      ipv6: "IPv6",
      ipVersion: "IP 协议",
      ipv6Hint: "未检测到 IPv6 地址，测速可能失败",
      noNodeForFamily: "该协议暂无可用节点",
      noAddrForFamily: "未获取到该协议的地址",
      lowestPing: "最低延迟",
      expWeb: "浏览网页",
      expGame: "游戏",
      expVideo: "视频",
      expCall: "视频通话",
      nodeAutoHint: "当前仅显示 {proto} 节点（{n} 个），已按延迟从低到高排序，默认自动选最低的",
      nodeOrderHint: "当前仅显示 {proto} 节点（{n} 个），按后台节点顺序排列（未启用自动选低延迟）",

      /* 设置面板 */
      globalSettings: "全局设置",
      currentConnection: "当前连接",
      testNode: "测速节点",
      spTime: "时间",
      spDate: "日期",
      spDistance: "距离",
      spSpeed: "速度",
      sp12: "12 小时",
      sp24: "24 小时",
      spMiles: "英里",
      spKm: "公里",
      spPreview: "预览：",
      spServer: "服务器",
      spNone: "未选择",
      lat: "纬度",
      lon: "经度",

      /* 提示 */
      pickServerFirst: "请先选择测速节点",
      linkCopied: "报告链接已复制",
      noResultYet: "还没有测速结果",
      summary: "延迟 {ping}ms · 下载 {down}Mbps · 上传 {up}Mbps",
      startTest: "开始测速",
      titleSuffixDefault: "网络测速",
      pageTitle: "{site} · {suffix}",

      /* 报告页 */
      share: "分享",
      reportTime: "测速时间",
      rateQ: "您向好友或同事推荐 {isp} 的可能性有多大？",
      rateQPrefix: "您向好友或同事推荐",
      rateQSuffix: "的可能性有多大？",
      rateNo: "根本不可能",
      rateYes: "非常有可能",
      thanks: "感谢您的评分（{n} 分）",
      backHome: "返回测速首页",
      reportTitle: "测速报告 {id} · {site}",
      reportFoot: "{site} · 报告编号 {id}",
      missingId: "缺少报告编号",
      notFound: "报告不存在或已过期",
      changeServer: "更换节点",
      shareToX: "分享到 X",
      shareToFB: "分享到 Facebook",
      moreShare: "更多分享方式",
      tlTitle: "逐秒曲线（上下行）",
      tlHint: "按住拖动查看每秒数据",
      tlSecond: "第 {n} 秒",
      tlNoData: "这份报告没有逐秒数据（旧版本记录）",
      histTitle: "测速历史",
      histEmpty: "这台设备还没有测速记录。测一次速，结果会自动记在这里。",
      histLoading: "正在读取这台设备上的测速记录…",
      histClear: "清空历史",
      histCleared: "历史已清空",
      histCount: "共 {n} 条",
      histPing: "延迟",
      histReport: "报告"
    },

    en: {
      menu: "Menu",
      results: "Results",
      settings: "Settings",
      admin: "Admin",
      downloadApp: "Download",
      signIn: "Sign In",
      langTitle: "Languages",
      langName: "English",
      languagesMenu: "Languages menu",
      toggleTheme: "Toggle theme",

      download: "Download",
      upload: "Upload",
      latency: "Latency",
      pingTip: "Ping",
      jitterTip: "Jitter",
      downLatTip: "Download latency",
      upLatTip: "Upload latency",
      lossTip: "Packet loss",

      resultId: "Result ID",
      copyLink: "Copy link",
      viewReport: "View report",

      yourNetwork: "Your Network",
      unknownIsp: "Unknown ISP",
      detecting: "Detecting…",
      offline: "Cannot reach server",
      noPublicIp: "No public IP detected",
      hidden: "Hidden",
      hiddenAdminOnly: "Hidden (admin only)",
      hiddenOwnerOnly: "Hidden (tester only)",
      trafficUsed: "Data used",

      finding: "Finding the best server…",
      noServer: "No servers available",
      serverFallback: "Speed Test Server",
      thisServer: "This server",
      more: "{n} more",
      change: "Change",
      toggleConnections: "Switch connections",
      selectServer: "Select a server",
      noServersInList: "No servers available",
      unavailable: "Unavailable",

      connections: "Connections",
      multi: "Multi",
      single: "Single",

      protocol: "Protocol",
      ipv4: "IPv4",
      ipv6: "IPv6",
      ipVersion: "IP version",
      ipv6Hint: "No IPv6 address detected — the test may fail",
      noNodeForFamily: "No servers available for this protocol",
      noAddrForFamily: "No address detected for this protocol",
      lowestPing: "Lowest ping",
      expWeb: "Web browsing",
      expGame: "Gaming",
      expVideo: "Video streaming",
      expCall: "Video calls",
      nodeAutoHint: "{n} {proto} servers, sorted by latency — the lowest is picked automatically",
      nodeOrderHint: "{n} {proto} servers in the admin list order (auto-pick disabled)",

      globalSettings: "Global settings",
      currentConnection: "Current connection",
      testNode: "Test server",
      spTime: "Time",
      spDate: "Date",
      spDistance: "Distance",
      spSpeed: "Speed",
      sp12: "12-hour",
      sp24: "24-hour",
      spMiles: "Miles",
      spKm: "Kilometers",
      spPreview: "Preview: ",
      spServer: "Server",
      spNone: "None",
      lat: "Lat",
      lon: "Lon",

      pickServerFirst: "Please select a server first",
      linkCopied: "Result link copied",
      noResultYet: "No test result yet",
      summary: "Latency {ping}ms · Download {down}Mbps · Upload {up}Mbps",
      startTest: "Start speed test",
      titleSuffixDefault: "Speed Test",
      pageTitle: "{site} · {suffix}",

      share: "Share",
      reportTime: "Tested at",
      rateQ: "How likely are you to recommend {isp} to a friend or colleague?",
      rateQPrefix: "How likely are you to recommend",
      rateQSuffix: "to a friend or colleague?",
      rateNo: "Not at all likely",
      rateYes: "Extremely likely",
      thanks: "Thanks for rating ({n})",
      backHome: "Back to speed test",
      reportTitle: "Speed test report {id} · {site}",
      reportFoot: "{site} · Result ID {id}",
      missingId: "Missing result ID",
      notFound: "Result not found or expired",
      changeServer: "Change Server",
      shareToX: "Share on X",
      shareToFB: "Share on Facebook",
      moreShare: "More sharing options",
      tlTitle: "Per-second timeline (DL / UL)",
      tlHint: "Drag to inspect each second",
      tlSecond: "Second {n}",
      tlNoData: "No per-second data for this report (older record)",
      histTitle: "Test history",
      histEmpty: "No tests on this device yet. Run a speed test and it will show up here.",
      histLoading: "Loading previous tests from this device…",
      histClear: "Clear history",
      histCleared: "History cleared",
      histCount: "{n} tests",
      histPing: "Ping",
      histReport: "Report"
    }
  };

  // 中文运营商名 -> 英文名。后端新记录会直接带 ispEn，
  // 老记录只有中文名，英文界面下用它兜底。
  var ISP_EN = {
    "中国电信": "China Telecom",
    "中国联通": "China Unicom",
    "中国移动": "China Mobile",
    "教育网": "CERNET",
    "阿里云": "Alibaba Cloud",
    "腾讯云": "Tencent Cloud",
    "华为云": "Huawei Cloud"
  };

  var STORAGE_KEY = "st_lang";
  var lang = "zh";

  // 后台自定义的前台路径段（由 /routes.js 注入），带默认值兜底。
  var R = (function () {
    var r = w.ST_ROUTES || {};
    return {
      langZh: r.langZh || "zh-Hans",
      langEn: r.langEn || "en",
      v4: r.v4 || "ipv4",
      v6: r.v6 || "ipv6",
      result: r.result || "result"
    };
  })();

  function detect() {
    // 分享链接里的语言前缀优先：/<en>/... 一定是英文，/<zh>/... 一定是中文。
    // 否则对方浏览器以前存过别的语言，地址栏和内容就对不上。
    var p = location.pathname;
    if (new RegExp("^/" + R.langEn + "(/|$)").test(p)) return "en";
    if (new RegExp("^/" + R.langZh + "(/|$)").test(p)) return "zh";
    var saved = null;
    try { saved = localStorage.getItem(STORAGE_KEY); } catch (e) {}
    if (saved === "zh" || saved === "en") return saved;
    var nav = (navigator.language || navigator.userLanguage || "").toLowerCase();
    return nav.indexOf("zh") === 0 ? "zh" : nav ? "en" : "zh";
  }

  function t(key, vars) {
    var table = DICT[lang] || DICT.zh;
    var s = table[key];
    if (s == null) s = (DICT.zh[key] != null ? DICT.zh[key] : key);
    if (vars) {
      for (var k in vars) {
        if (Object.prototype.hasOwnProperty.call(vars, k)) {
          s = s.split("{" + k + "}").join(vars[k]);
        }
      }
    }
    return s;
  }

  // 把带 data-i18n / data-i18n-aria / data-i18n-title 的节点刷成当前语言
  function apply(root) {
    var scope = root || document;
    var nodes = scope.querySelectorAll("[data-i18n]");
    for (var i = 0; i < nodes.length; i++) {
      nodes[i].textContent = t(nodes[i].getAttribute("data-i18n"));
    }
    nodes = scope.querySelectorAll("[data-i18n-aria]");
    for (var j = 0; j < nodes.length; j++) {
      nodes[j].setAttribute("aria-label", t(nodes[j].getAttribute("data-i18n-aria")));
    }
    nodes = scope.querySelectorAll("[data-i18n-title]");
    for (var k = 0; k < nodes.length; k++) {
      nodes[k].setAttribute("title", t(nodes[k].getAttribute("data-i18n-title")));
    }
    document.documentElement.setAttribute("lang", lang === "en" ? "en" : "zh-CN");
  }

  // 路径上的语言前缀，切语言时顺手改地址栏，刷新和分享出去的链接都会带着正确的语言。
  function prefix() {
    return "/" + (lang === "en" ? R.langEn : R.langZh);
  }

  // 转义正则特殊字符（自定义段一般只有字母数字，这里保险处理）。
  function rx(s) { return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"); }

  function syncPath() {
    try {
      var p = location.pathname;
      var langRe = "^/(" + rx(R.langZh) + "|" + rx(R.langEn) + ")(/|$)";
      var hasPrefix = new RegExp(langRe).test(p);
      var next = p;
      if (hasPrefix) {
        next = p.replace(new RegExp("^/(" + rx(R.langZh) + "|" + rx(R.langEn) + ")(?=/|$)"), prefix());
      } else if (p === "/" ||
                 new RegExp("^/" + rx(R.v4) + "/?$").test(p) ||
                 new RegExp("^/" + rx(R.v6) + "/?$").test(p) ||
                 new RegExp("^/" + rx(R.v4) + "/" + rx(R.result) + "/").test(p) ||
                 new RegExp("^/" + rx(R.v6) + "/" + rx(R.result) + "/").test(p) ||
                 new RegExp("^/" + rx(R.result) + "/").test(p)) {
        // 没带语言前缀的老链接补一个
        next = prefix() + (p === "/" ? "/" : p);
      }
      if (next !== p) history.replaceState(null, "", next + location.search + location.hash);
    } catch (e) {}
  }

  function set(next) {
    if (next !== "zh" && next !== "en") return;
    lang = next;
    try { localStorage.setItem(STORAGE_KEY, lang); } catch (e) {}
    apply();
    syncPath();
    var label = document.getElementById("langLabel");
    if (label) label.textContent = t("langName");
    var items = document.querySelectorAll("[data-lang]");
    for (var i = 0; i < items.length; i++) {
      items[i].classList.toggle("on", items[i].getAttribute("data-lang") === lang);
    }
    document.dispatchEvent(new CustomEvent("langchange", { detail: { lang: lang } }));
  }

  // 按当前界面语言取运营商名：中文界面用中文名，英文界面优先用英文名
  function ispName(zh, en) {
    zh = zh || "";
    en = en || ISP_EN[zh] || "";
    return lang === "en" ? (en || zh) : (zh || en);
  }

  lang = detect();
  // 老链接没带语言前缀时，按当前语言补一个（/ → /zh-Hans/ 或 /en/）
  syncPath();

  w.I18N = {
    t: t,
    set: set,
    apply: apply,
    ispName: ispName,
    prefix: prefix,
    routes: R,
    get lang() { return lang; }
  };
})(window);
