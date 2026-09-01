(() => {
  "use strict";

  const tabs = [
    { btn: document.getElementById("nav-peta"), view: document.getElementById("view-peta"), name: "peta", path: "/" },
    { btn: document.getElementById("nav-daftar"), view: document.getElementById("view-daftar"), name: "daftar", path: "/daftar" },
  ];

  function tabForPath(path) {
    return tabs.find((t) => t.path === path) || tabs[0];
  }

  // pushState: false is used for the initial paint (nothing to push yet)
  // and for popstate (the browser already changed the URL for us — pushing
  // again would create a duplicate history entry and break Back).
  function show(name, { pushState = true } = {}) {
    const tab = tabs.find((t) => t.name === name) || tabs[0];
    for (const t of tabs) {
      const active = t === tab;
      t.btn.classList.toggle("active", active);
      t.view.classList.toggle("active", active);
    }
    if (pushState && window.location.pathname !== tab.path) {
      history.pushState({ view: tab.name }, "", tab.path);
    }
    // Let the now-visible view fix up anything that only makes sense once
    // it has a real size on screen (Leaflet's invalidateSize, a deferred
    // first data load, etc).
    document.dispatchEvent(new CustomEvent(`view:${tab.name}-shown`));
  }

  for (const t of tabs) {
    t.btn.addEventListener("click", () => show(t.name));
  }

  // Back/forward: the URL already changed, just sync the visible view to it.
  window.addEventListener("popstate", () => {
    show(tabForPath(window.location.pathname).name, { pushState: false });
  });

  // Respect whatever URL the page was actually loaded on — a direct visit
  // or refresh of /daftar should land on Daftar, not always default to Peta.
  show(tabForPath(window.location.pathname).name, { pushState: false });
})();
