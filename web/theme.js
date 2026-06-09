// Sets the theme before first paint: a saved preference wins, otherwise the
// system theme. Lives in its own file so the page works under a CSP without
// inline scripts.
(function () {
  var saved = null;
  try {
    saved = localStorage.getItem("branch-theme");
  } catch (_) {}
  var dark = saved ? saved === "dark" : window.matchMedia("(prefers-color-scheme: dark)").matches;
  document.documentElement.dataset.theme = dark ? "dark" : "light";
})();
