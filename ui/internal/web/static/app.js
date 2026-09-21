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

  // Sayfada birden çok tema anahtarı olabilir (sol nav + üst çubuk + mobil);
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

      // Sol nav bloğu KARŞI temayı yazar ("Koyu temaya geç" eylemi).
      var text = el.querySelector(".theme-toggle-text");
      var textLabel = el.getAttribute("data-text-" + opposite);
      if (text && textLabel) text.textContent = textLabel;

      // Üst çubuk pill'i AKTİF temayı yazar (referans variant="header").
      var mode = el.querySelector(".theme-toggle-mode");
      var modeLabel = el.getAttribute("data-mode-" + nextTheme);
      if (mode && modeLabel) mode.textContent = modeLabel;
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

// ------------------------------------------------- arama kutusu temizle (X)
// JS yokken düğme hiç görünmez (markup'ta hidden, CSS'te [hidden] gizler);
// arama kutusu doğal biçimde elle boşaltılıp gönderilebilir. JS varken düğme
// yalnız kutuda metin varken belirir, tıklanınca kutuyu boşaltıp odağı geri
// verir — form kendiliğinden gönderilmez.
(function () {
  "use strict";

  var input = document.querySelector(".search-input");
  var button = document.querySelector("[data-search-clear]");
  if (!input || !button) return;

  function sync() {
    button.hidden = input.value.length === 0;
  }

  sync();
  input.addEventListener("input", sync);
  // type=search'te Escape/tarayıcı temizlemesi "input" üretmeyebilir.
  input.addEventListener("search", sync);

  button.addEventListener("click", function () {
    input.value = "";
    sync();
    input.focus();
  });
})();

// ------------------------------------------------- alıntıyı panoya kopyala
// Kopyalanan metin ayrı bir data attribute'ta tutulmaz; düğme kendi kanıt
// kartındaki <blockquote> içeriğini okur, böylece alıntı birebir kalır.
// Clipboard API yoksa (JS kapalı ya da güvensiz bağlam) .has-clipboard hiç
// eklenmez ve düğmeler gizli kalır.
(function () {
  "use strict";

  var buttons = document.querySelectorAll("[data-quote-copy]");
  if (!buttons.length) return;

  var clipboard = navigator.clipboard;
  if (!clipboard || typeof clipboard.writeText !== "function") return;

  document.documentElement.classList.add("has-clipboard");

  var RESET_MS = 2000;
  var timers = new WeakMap();

  function label(button, name) {
    var text = button.getAttribute("data-" + name + "-label") || "";
    var slot = button.querySelector("[data-quote-copy-text]");
    if (slot) slot.textContent = text;
    if (text) button.setAttribute("title", text);
  }

  function confirmCopy(button) {
    button.classList.add("is-copied");
    label(button, "copied");

    clearTimeout(timers.get(button));
    timers.set(button, setTimeout(function () {
      button.classList.remove("is-copied");
      label(button, "copy");
    }, RESET_MS));
  }

  Array.prototype.forEach.call(buttons, function (button) {
    button.addEventListener("click", function () {
      var card = button.closest(".quote");
      var quote = card ? card.querySelector(".quote-text") : null;
      if (!quote) return;

      clipboard.writeText(quote.textContent).then(
        function () { confirmCopy(button); },
        function () { /* pano reddetti: sessizce geç, düğme aynı kalır */ }
      );
    });
  });
})();

// --------------------------------------------------------- Idea Copilot
// Panel JS olmadan da tam çalışır: hızlı komut çipleri ve giriş alanı
// gerçek <form method="post"> gönderimidir, sunucu 303 ile karta döner.
// Buradaki iş üç başlık: (1) mobil/tablet çekmecesi, (2) sayfa yenilemeden
// mesaj gönderimi, (3) cevapla gelen öneri çipleri. Inline script yoktur.
(function () {
  "use strict";

  var root = document.documentElement;
  // Çekmece yalnız JS varken devreye girer; CSS bu sınıfa bakar.
  root.classList.add("has-js");

  var panel = document.querySelector("[data-chat-panel]");
  if (!panel) return;

  var body = document.body;
  var log = panel.querySelector("[data-chat-log]");
  var form = panel.querySelector("[data-chat-form]");
  var blendForm = panel.querySelector("[data-chat-blend]");
  var status = panel.querySelector("[data-chat-status]");
  var input = panel.querySelector(".chat-input");
  var sendButton = panel.querySelector(".chat-send");
  var openers = document.querySelectorAll("[data-chat-open]");
  var closers = document.querySelectorAll("[data-chat-close]");
  var backdrop = document.querySelector(".chat-backdrop");
  var lastOpener = null;
  var busy = false;

  // Sayfa açılışında en yeni mesaj görünür olsun (referans: scrollToBottom).
  if (log) log.scrollTop = log.scrollHeight;

  function isDrawerViewport() {
    return window.matchMedia("(max-width: 1023.98px)").matches;
  }

  function openChat(trigger) {
    lastOpener = trigger || null;
    if (isDrawerViewport()) {
      body.classList.add("chat-open");
      if (backdrop) backdrop.hidden = false;
    } else {
      panel.scrollIntoView({ block: "nearest" });
    }
    if (input) input.focus();
  }

  function closeChat() {
    if (!body.classList.contains("chat-open")) return;
    body.classList.remove("chat-open");
    if (backdrop) backdrop.hidden = true;
    if (lastOpener && typeof lastOpener.focus === "function") lastOpener.focus();
  }

  Array.prototype.forEach.call(openers, function (el) {
    el.addEventListener("click", function (event) {
      if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
      event.preventDefault();
      openChat(el);
    });
  });

  Array.prototype.forEach.call(closers, function (el) {
    el.addEventListener("click", function (event) {
      event.preventDefault();
      closeChat();
    });
  });

  document.addEventListener("keydown", function (event) {
    if (event.key === "Escape") closeChat();
  });

  // Çekmece açıkken masaüstü genişliğine geçilirse durum sıfırlanır.
  window.addEventListener("resize", function () {
    if (!isDrawerViewport() && body.classList.contains("chat-open")) {
      body.classList.remove("chat-open");
      if (backdrop) backdrop.hidden = true;
    }
  });

  // ---------------------------------------------------------- yardımcılar
  function message(name) {
    return panel.getAttribute("data-msg-" + name) || panel.getAttribute("data-msg-failed") || "";
  }

  function showStatus(text) {
    if (!status) return;
    status.textContent = text;
    status.hidden = !text;
  }

  function setBusy(next) {
    busy = next;
    if (input) input.disabled = next;
    if (sendButton) sendButton.disabled = next;
    Array.prototype.forEach.call(panel.querySelectorAll(".chat-chip"), function (chip) {
      chip.disabled = next;
    });
    var blendButton = panel.querySelector(".chat-blend-button");
    if (blendButton) blendButton.disabled = next;
  }

  function clearEmptyState() {
    var empty = log ? log.querySelector(".chat-empty") : null;
    if (empty) empty.remove();
  }

  function clearSuggestions() {
    Array.prototype.forEach.call(panel.querySelectorAll(".chat-suggestions"), function (el) {
      el.remove();
    });
  }

  function clockNow() {
    var d = new Date();
    function pad(n) { return (n < 10 ? "0" : "") + n; }
    return pad(d.getUTCHours()) + ":" + pad(d.getUTCMinutes());
  }

  var ICON_USER =
    '<svg viewBox="0 0 24 24" width="14" height="14" focusable="false">' +
    '<circle cx="12" cy="8.5" r="3.6" fill="currentColor"/>' +
    '<path d="M4.8 20a7.2 7.2 0 0 1 14.4 0" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"/></svg>';
  var ICON_BOT =
    '<svg viewBox="0 0 24 24" width="14" height="14" focusable="false">' +
    '<path d="m12 3 1.9 4.6L18.5 9.5 13.9 11.4 12 16l-1.9-4.6L5.5 9.5l4.6-1.9L12 3Z" fill="currentColor"/></svg>';

  // appendMessage, metni DÜĞÜM olarak basar (textContent): sunucu tarafındaki
  // escape kuralının aynısı — hiçbir mesaj HTML olarak yorumlanmaz.
  function appendMessage(role, text) {
    if (!log) return;
    clearEmptyState();

    var isUser = role === "user";
    var row = document.createElement("div");
    row.className = "chat-msg" + (isUser ? " is-user" : "");

    var avatar = document.createElement("span");
    avatar.className = "chat-msg-avatar";
    avatar.setAttribute("aria-hidden", "true");
    avatar.innerHTML = isUser ? ICON_USER : ICON_BOT;

    var wrap = document.createElement("div");
    wrap.className = "chat-bubble-wrap";

    var bubble = document.createElement("p");
    bubble.className = "chat-bubble";
    var label = document.createElement("span");
    label.className = "sr-only";
    label.textContent =
      (isUser
        ? panel.getAttribute("data-label-you")
        : panel.getAttribute("data-label-assistant")) + ": ";
    bubble.appendChild(label);
    // Satır sonları CSS ile değil, sunucudaki gibi <br> ile korunur.
    text.split(/\r\n|\r|\n/).forEach(function (line, idx) {
      if (idx > 0) bubble.appendChild(document.createElement("br"));
      bubble.appendChild(document.createTextNode(line));
    });

    var time = document.createElement("span");
    time.className = "chat-time";
    time.textContent = clockNow();

    wrap.appendChild(bubble);
    wrap.appendChild(time);
    row.appendChild(avatar);
    row.appendChild(wrap);
    log.appendChild(row);
    log.scrollTop = log.scrollHeight;
    return wrap;
  }

  function appendSuggestions(host, items) {
    if (!host || !items || !items.length) return;
    var box = document.createElement("div");
    box.className = "chat-suggestions";
    items.slice(0, 3).forEach(function (text) {
      var chip = document.createElement("button");
      chip.type = "button";
      chip.className = "chat-chip chat-suggestion";
      chip.textContent = text;
      chip.addEventListener("click", function () {
        send(text);
      });
      box.appendChild(chip);
    });
    host.appendChild(box);
    log.scrollTop = log.scrollHeight;
  }

  // ------------------------------------------------------------ gönderim
  function send(text) {
    if (busy || !form) return;
    var msg = (text || "").trim();
    if (!msg) return;

    clearSuggestions();
    appendMessage("user", msg);
    if (input) input.value = "";
    setBusy(true);
    showStatus(message("generating"));

    // urlencoded gövde: sunucu tarafı form gönderimiyle AYNI yolu kullanır
    // (multipart olsaydı ParseForm okumazdı).
    var payload = new URLSearchParams();
    payload.append("message", msg);

    fetch(form.action, {
      method: "POST",
      headers: { Accept: "application/json" },
      credentials: "same-origin",
      body: payload
    })
      .then(function (res) {
        return res.json().then(
          function (data) { return { ok: res.ok, data: data }; },
          function () { return { ok: false, data: {} }; }
        );
      })
      .then(function (result) {
        setBusy(false);
        if (!result.ok) {
          showStatus(result.data.message || message(result.data.error || "failed"));
          return;
        }
        showStatus("");
        var reply = result.data.reply || {};
        var host = appendMessage("assistant", reply.message || "");
        appendSuggestions(host, result.data.suggestions);
      })
      .catch(function () {
        setBusy(false);
        showStatus(message("failed"));
      });
  }

  form.addEventListener("submit", function (event) {
    event.preventDefault();
    send(input ? input.value : "");
  });

  // Hızlı komut çipleri: JS varken sayfayı yenilemeden gönderilir.
  Array.prototype.forEach.call(panel.querySelectorAll(".chat-quick .chat-chip"), function (chip) {
    // JS'siz fallback için markup'ta type="submit" duruyor; JS varken bunu
    // "button"a çevirmezsek çip, form'un implicit-submit varsayılan butonu
    // olur ve kutuda Enter'a basınca çipin sabit metni gönderilir (#84).
    chip.type = "button";
    chip.addEventListener("click", function (event) {
      event.preventDefault();
      send(chip.value);
    });
  });

  // "Kart olarak türet" doğal gönderimle gider (yeni karta yönlendirir);
  // yalnız bekleme durumu gösterilir.
  if (blendForm) {
    blendForm.addEventListener("submit", function () {
      setBusy(true);
      showStatus(message("generating"));
    });
  }
})();
