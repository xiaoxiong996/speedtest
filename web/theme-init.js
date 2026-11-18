/* 首屏主题-init：在 CSS 加载前应用本地保存的主题，避免闪黑/闪白。
   CSP 为 script-src 'self'（禁止内联），所以独立成文件由 <script src> 引入。 */
(function () {
  var t = "dark";
  try {
    var v = localStorage.getItem("st_theme");
    if (v === "light" || v === "dark") t = v;
  } catch (e) {}
  document.documentElement.setAttribute("data-theme", t);
  var m = document.querySelector('meta[name="theme-color"]');
  if (m) m.setAttribute("content", t === "light" ? "#f4f5f7" : "#000000");
})();
