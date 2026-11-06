// The Hub's only script: theme toggle, filter auto-submit, and a quiet
// refresh of <main> every 10 seconds on live pages while the tab is visible.
(function () {
  var root = document.documentElement;
  document.addEventListener("click", function (event) {
    var button = event.target.closest("[data-theme-toggle]");
    if (!button) return;
    var dark = root.dataset.theme ? root.dataset.theme === "dark" : !matchMedia("(prefers-color-scheme: light)").matches;
    root.dataset.theme = dark ? "light" : "dark";
    try { localStorage.setItem("hub-theme", root.dataset.theme); } catch (e) {}
  });
  document.addEventListener("change", function (event) {
    var form = event.target.closest("form[data-autosubmit]");
    if (form) form.submit();
  });
  var busy = false;
  setInterval(function () {
    var main = document.getElementById("main");
    if (busy || !main || !main.hasAttribute("data-live") || document.hidden) return;
    if (document.activeElement && document.activeElement.closest("select, input")) return;
    busy = true;
    fetch(location.href, { headers: { "X-Hub-Refresh": "1" }, credentials: "same-origin" })
      .then(function (response) { return response.ok ? response.text() : null; })
      .then(function (html) {
        if (!html) return;
        var next = new DOMParser().parseFromString(html, "text/html").getElementById("main");
        if (!next) return;
        var oldFlow = main.querySelector("[data-flow]");
        var newFlow = next.querySelector("[data-flow]");
        var keep = oldFlow && newFlow && oldFlow.dataset.flow === newFlow.dataset.flow;
        if (keep) newFlow.replaceWith(oldFlow); // same graph and statuses: keep the canvas and its zoom
        main.innerHTML = "";
        while (next.firstChild) main.appendChild(next.firstChild);
        if (!next.hasAttribute("data-live")) main.removeAttribute("data-live");
        document.dispatchEvent(new Event("hub:refresh"));
      })
      .catch(function () {})
      .then(function () { busy = false; });
  }, 10000);
})();
