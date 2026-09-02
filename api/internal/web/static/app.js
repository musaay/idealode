// IdeaLode — hafif progressive enhancement.
//
// Sayfa JS olmadan da tamamen çalışır: dil ve tema bağlantıları gerçek
// <a href="?lang=..."> / <a href="?theme=..."> adresleridir, sunucu cookie'yi
// yazar. Buradaki tek iş, tema geçişini sayfa yeniden yüklenmeden yapmak.
// Inline script yoktur (CSP uyumlu).
(function () {
  "use strict";

  var toggles = document.querySelectorAll(".theme-toggle");
  if (!toggles.length) return;

  var root = document.documentElement;
  var YEAR = 365 * 24 * 60 * 60;

  function writeCookie(name, value) {
    var secure = location.protocol === "https:" ? "; Secure" : "";
    document.cookie =
      name + "=" + encodeURIComponent(value) + "; Path=/; Max-Age=" + YEAR +
      "; SameSite=Lax" + secure;
  }

  // Sayfada birden çok tema anahtarı olabilir (sol nav + mobil header);
  // hepsi aynı durumu gösterir, biri değişince hepsi güncellenir.
  function sync(nextTheme) {
    var opposite = nextTheme === "dark" ? "light" : "dark";
    Array.prototype.forEach.call(toggles, function (el) {
      el.setAttribute("data-next-theme", opposite);

      var label = el.getAttribute("data-label-" + opposite);
      if (label) el.setAttribute("aria-label", label);

      var url = new URL(el.href, location.href);
      url.searchParams.set("theme", opposite);
      el.setAttribute("href", url.pathname + url.search);

      var text = el.querySelector(".theme-toggle-text");
      var textLabel = el.getAttribute("data-text-" + opposite);
      if (text && textLabel) text.textContent = textLabel;
    });
  }

  function onClick(event) {
    // Yeni sekmede açma / modifier'lı tıklamalarda tarayıcıya karışma.
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey ||
        event.button !== 0) {
      return;
    }

    var next = this.getAttribute("data-next-theme");
    if (next !== "light" && next !== "dark") return;

    event.preventDefault();
    root.setAttribute("data-theme", next);
    writeCookie("theme", next);
    sync(next);
  }

  Array.prototype.forEach.call(toggles, function (el) {
    el.addEventListener("click", onClick);
  });
})();
