// Applies the light/dark choice before the page is drawn (this script is
// not deferred), and runs the switch in the header. Without a saved choice
// the system setting decides.
(function () {
  var root = document.documentElement;
  try {
    var saved = localStorage.getItem("bs-theme");
    if (saved === "light" || saved === "dark") root.dataset.theme = saved;
  } catch (e) {}

  document.addEventListener("click", function (e) {
    if (!e.target.closest || !e.target.closest("[data-theme-toggle]")) return;
    var dark = root.dataset.theme ? root.dataset.theme === "dark" : matchMedia("(prefers-color-scheme: dark)").matches;
    root.dataset.theme = dark ? "light" : "dark";
    try { localStorage.setItem("bs-theme", root.dataset.theme); } catch (e) {}
    document.dispatchEvent(new Event("bs-theme")); // the chart and map redraw in the new colors
  });
})();
