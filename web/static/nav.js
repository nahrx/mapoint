(() => {
  "use strict";

  const tabs = [
    { btn: document.getElementById("nav-peta"), view: document.getElementById("view-peta"), name: "peta" },
    { btn: document.getElementById("nav-daftar"), view: document.getElementById("view-daftar"), name: "daftar" },
  ];

  function show(name) {
    for (const t of tabs) {
      const active = t.name === name;
      t.btn.classList.toggle("active", active);
      t.view.classList.toggle("active", active);
    }
    // Let the now-visible view fix up anything that only makes sense once
    // it has a real size on screen (Leaflet's invalidateSize, a deferred
    // first data load, etc).
    document.dispatchEvent(new CustomEvent(`view:${name}-shown`));
  }

  for (const t of tabs) {
    t.btn.addEventListener("click", () => show(t.name));
  }
})();
