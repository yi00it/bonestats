// Dashboard behaviour: chart, map and small form helpers. Everything except
// the chart and map also works without JavaScript.
var css = getComputedStyle(document.documentElement);
var cssVar = function (n) { return css.getPropertyValue(n).trim(); };

function bindForms(root) {
  root.querySelectorAll("[data-autosubmit]").forEach(function (el) {
    el.addEventListener("change", function () {
      var custom = document.querySelector(".custom");
      if (el.name === "range" && el.value === "custom" && custom) {
        custom.removeAttribute("data-custom-hidden"); // pick dates, then Apply
        return;
      }
      el.form.submit();
    });
  });
  root.querySelectorAll("form[data-confirm]").forEach(function (f) {
    f.addEventListener("submit", function (e) { if (!confirm(f.dataset.confirm)) e.preventDefault(); });
  });
}
bindForms(document);

// Show country names instead of ISO codes.
var regionNames = window.Intl && Intl.DisplayNames && new Intl.DisplayNames(["en"], { type: "region" });
function countryName(code) {
  try { return (regionNames && regionNames.of(code)) || code; } catch (e) { return code; }
}

// Adds "dim is value" to the current URL, replacing filters on the same dimension.
function filterURL(dim, value) {
  var p = new URLSearchParams(location.search), keep = p.getAll("f").filter(function (f) { return f.split("|")[0] !== dim; });
  p.delete("f");
  keep.concat(dim + "|is|" + value).forEach(function (f) { p.append("f", f); });
  return "/?" + p.toString();
}

function hex(c) {
  var m = c.replace("#", "").match(/../g) || ["88", "88", "88"];
  return m.map(function (x) { return parseInt(x, 16); });
}

function formatValue(kind, v) {
  if (v == null) return "";
  switch (kind) {
    case "percent": return Math.round(v) + "%";
    case "decimal": return v.toFixed(1);
    case "duration":
      v = Math.round(v);
      return v < 60 ? v + "s" : Math.floor(v / 60) + "m " + String(v % 60).padStart(2, "0") + "s";
    default: return v >= 1e4 ? (v / 1e3).toFixed(1) + "k" : String(Math.round(v));
  }
}

function chart() {
  var el = document.getElementById("chart"), raw = document.getElementById("chart-data");
  if (!el || !raw || !window.uPlot) return;
  var cfg = JSON.parse(raw.textContent);
  var fmtTime = {
    hour: function (d) { return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }); },
    day: function (d) { return d.toLocaleDateString([], { weekday: "short", month: "short", day: "numeric" }); },
    week: function (d) { return "Week of " + d.toLocaleDateString([], { month: "short", day: "numeric" }); },
    month: function (d) { return d.toLocaleDateString([], { month: "long", year: "numeric" }); }
  }[cfg.interval];
  var axis = { stroke: cssVar("--muted"), grid: { stroke: cssVar("--line"), width: 1 }, ticks: { show: false } };
  var series = [
    { value: function (u, t) { return t == null ? "" : fmtTime(new Date(t * 1000)); } },
    { label: cfg.label, stroke: cssVar("--accent"), width: 2, fill: cssVar("--accent-fill"), points: { show: false },
      value: function (u, v) { return formatValue(cfg.kind, v); } }
  ];
  var data = [cfg.t, cfg.cur];
  if (cfg.prev) {
    series.push({ label: cfg.prevLabel, stroke: cssVar("--accent-2"), width: 1.5, dash: [4, 4], points: { show: false },
      value: function (u, v) { return formatValue(cfg.kind, v); } });
    data.push(cfg.prev);
  }
  var opts = {
    width: el.clientWidth, height: 260,
    cursor: { y: false, points: { size: 7 } },
    scales: {
      x: { time: true },
      y: { range: function (u, min, max) { return [0, cfg.kind === "percent" ? 100 : Math.max(cfg.kind === "decimal" ? 1 : 4, max * 1.15)]; } }
    },
    axes: [
      Object.assign({}, axis, { grid: { show: false } }),
      Object.assign({}, axis, { size: 52, values: function (u, vals) { return vals.map(function (v) { return formatValue(cfg.kind, v); }); } })
    ],
    series: series
  };
  if (cfg.interval === "month") {
    opts.axes[0].values = function (u, vals) { return vals.map(function (t) { return new Date(t * 1000).toLocaleDateString([], { month: "short" }); }); };
  }
  var plot = new uPlot(opts, data, el);
  var ro = new ResizeObserver(function () { plot.setSize({ width: el.clientWidth, height: 260 }); });
  ro.observe(el);
  return function () { ro.disconnect(); plot.destroy(); };
}

function map() {
  var el = document.getElementById("map"), raw = document.getElementById("map-data");
  if (!el || !raw || !window.jsVectorMap) return;
  var data = JSON.parse(raw.textContent);
  // jsvectormap only supports named color buckets: shade countries in 5 steps
  // from light to the accent color, on a square-root scale.
  var max = Math.max.apply(null, Object.values(data).concat(1));
  var lo = hex(cssVar("--map-low")), hi = hex(cssVar("--accent")), scale = {}, buckets = {};
  for (var i = 0; i < 5; i++) {
    scale["b" + i] = "rgb(" + lo.map(function (c, j) { return Math.round(c + (hi[j] - c) * i / 4); }).join(",") + ")";
  }
  Object.keys(data).forEach(function (code) { buckets[code] = "b" + Math.min(4, Math.floor(Math.sqrt(data[code] / max) * 5)); });
  var m = new jsVectorMap({
    selector: "#map",
    map: "world",
    zoomButtons: false,
    zoomOnScroll: false,
    showTooltip: true,
    regionStyle: {
      initial: { fill: cssVar("--map-empty"), stroke: cssVar("--card"), strokeWidth: 0.4 },
      hover: { fillOpacity: 0.8, cursor: "pointer" }
    },
    series: { regions: [{ attribute: "fill", scale: scale, values: buckets }] },
    onRegionTooltipShow: function (event, tooltip, code) {
      var n = data[code] || 0;
      tooltip.text(countryName(code) + ": " + n + " visitor" + (n === 1 ? "" : "s"));
    },
    onRegionClick: function (event, code) {
      if (data[code]) location.href = filterURL("country", code);
    }
  });
  return function () { m.destroy(); };
}

function countryNames(root) {
  root.querySelectorAll("[data-country]").forEach(function (el) {
    el.textContent = countryName(el.dataset.country);
  });
}

countryNames(document);
var stopChart = chart(), stopMap = map();

// Live updates: refetch the dashboard every 15 seconds while it is visible
// and swap in the new <main>. Open tabs are kept, and the chart and map are
// only redrawn when their data changed. Skipped while the user is busy with
// the toolbar or the filter form.
(function live() {
  var main = document.querySelector("main[data-live]");
  if (!main || !window.fetch || !window.DOMParser) return;
  var every = 15000, last = Date.now(), busy = false;

  function interacting() {
    var a = document.activeElement;
    return main.querySelector("details[open]") || (a && main.contains(a) && a.matches("select, textarea, input:not([type=radio])"));
  }

  function refresh() {
    if (busy || document.hidden || interacting()) return;
    busy = true;
    fetch(location.href, { cache: "no-store", credentials: "same-origin" }).then(function (res) {
      // A redirect means the login expired or the site is gone: reload properly.
      if (res.redirected || !res.ok) {
        if (res.redirected) location.reload();
        return;
      }
      return res.text().then(function (html) {
        var next = new DOMParser().parseFromString(html, "text/html").querySelector("main");
        if (!next || interacting()) return;
        var checked = Array.prototype.map.call(main.querySelectorAll("input[type=radio]:checked"), function (r) { return r.id; });
        var keep = function (id) {
          var a = document.getElementById(id + "-data"), b = next.querySelector("#" + id + "-data");
          if (!a || !b || a.textContent !== b.textContent) return false;
          next.querySelector("#" + id).replaceWith(document.getElementById(id));
          return true;
        };
        var keepChart = keep("chart"), keepMap = keep("map");
        if (!keepChart && stopChart) stopChart();
        if (!keepMap && stopMap) stopMap();
        main.replaceChildren.apply(main, Array.prototype.slice.call(next.childNodes));
        checked.forEach(function (id) { var r = document.getElementById(id); if (r) r.checked = true; });
        countryNames(main);
        bindForms(main);
        if (!keepChart) stopChart = chart();
        if (!keepMap) stopMap = map();
      });
    }).catch(function () {}).finally(function () { busy = false; last = Date.now(); });
  }

  setInterval(function () { if (Date.now() - last >= every) refresh(); }, 1000);
  document.addEventListener("visibilitychange", function () {
    if (!document.hidden && Date.now() - last >= every) refresh();
  });
})();
