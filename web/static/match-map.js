(() => {
  "use strict";

  const { fetchWithRetry, makeCascadingLevel, esc } = App;

  const statsEl = document.getElementById("match-stats");
  const loadingEl = document.getElementById("match-loading");
  const panelEl = document.getElementById("match-panel");
  const panelToggleBtn = document.getElementById("match-panel-toggle");
  const kabkotaSelect = document.getElementById("kabkota-select-match");
  const kecamatanSelect = document.getElementById("kecamatan-select-match");
  const desaSelect = document.getElementById("desa-select-match");
  const slsSelect = document.getElementById("sls-select-match");
  const subslsSelect = document.getElementById("subsls-select-match");
  const matchStatusSelect = document.getElementById("matchstatus-select-match");
  const searchInput = document.getElementById("match-search");
  const applyFilterBtn = document.getElementById("apply-filter-btn-match");

  const isNarrow = window.matchMedia("(max-width: 600px)").matches;

  // Same construction choices as the main map: zoom control moved off the
  // top-left so it isn't buried under the filter panel, and a compact
  // basemap switcher on a phone. See app.js for the reasoning.
  const map = L.map("matchmap", {
    preferCanvas: true,
    worldCopyJump: true,
    zoomControl: false,
  }).setView([-1.0, 117.0], 8);

  L.control.zoom({ position: "bottomright" }).addTo(map);

  // This view starts hidden, so Leaflet measures its container as 0x0 until
  // nav.js shows it — invalidateSize on every reveal fixes that.
  document.addEventListener("view:petamatch-shown", () => map.invalidateSize());

  function setPanelCollapsed(collapsed) {
    panelEl.classList.toggle("collapsed", collapsed);
    panelToggleBtn.setAttribute("aria-expanded", String(!collapsed));
    panelToggleBtn.title = collapsed ? "Tampilkan filter" : "Sembunyikan filter";
  }
  panelToggleBtn.addEventListener("click", () => {
    setPanelCollapsed(!panelEl.classList.contains("collapsed"));
  });
  if (isNarrow) setPanelCollapsed(true);

  const osmLayer = L.tileLayer("https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png", {
    maxZoom: 19,
    attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors',
  });
  const satelliteLayer = L.tileLayer(
    "https://server.arcgisonline.com/ArcGIS/rest/services/World_Imagery/MapServer/tile/{z}/{y}/{x}",
    {
      maxZoom: 19,
      attribution: "Tiles &copy; Esri &mdash; Source: Esri, Maxar, Earthstar Geographics, and the GIS User Community",
    }
  );
  const satelliteLabels = L.tileLayer(
    "https://server.arcgisonline.com/ArcGIS/rest/services/Reference/World_Boundaries_and_Places/MapServer/tile/{z}/{y}/{x}",
    { maxZoom: 19 }
  );

  osmLayer.addTo(map);
  L.control.layers(
    {
      "Peta Jalan (OpenStreetMap)": osmLayer,
      "Satelit (Esri)": L.layerGroup([satelliteLayer, satelliteLabels]),
    },
    null,
    { collapsed: isNarrow, position: "bottomleft" }
  ).addTo(map);

  const canvasRenderer = L.canvas({ padding: 0.5 });
  const layer = L.layerGroup().addTo(map);

  let kabkotaByCode = new Map();
  let kecamatanByCode = new Map();
  let desaByCode = new Map();
  let slsByCode = new Map();
  let subslsByCode = new Map();

  // Only the Terapkan Filter button promotes the dropdowns into these, the
  // same split the other menus use.
  let appliedKabkota = "";
  let appliedKecamatan = "";
  let appliedDesa = "";
  let appliedSls = "";
  let appliedSubsls = "";
  let appliedMatchStatus = "";
  let appliedSearch = "";

  const EMPTY_VALUE = "__EMPTY__";

  function tooltipHTML(p) {
    return `
      <div class="titik-tooltip">
        <div><b>Nama Prelist:</b> ${esc(p.nama)}</div>
        <div><b>Nama KK:</b> ${esc(p.nama_kk)}</div>
        <div><b>ID SubSLS:</b> ${esc(p.subsls)}</div>
        <div><b>Alamat:</b> ${esc(p.alamat)}</div>
      </div>`;
  }

  // --- fetching ----------------------------------------------------------

  let inFlightController = null;
  let debounceTimer = null;
  let requestSeq = 0;

  function scheduleLoad() {
    clearTimeout(debounceTimer);
    debounceTimer = setTimeout(loadViewport, 250);
  }

  function filterParams() {
    const params = new URLSearchParams();
    if (appliedKabkota) params.set("kabkota", appliedKabkota);
    if (appliedKecamatan) params.set("kecamatan", appliedKecamatan);
    if (appliedDesa) params.set("desa", appliedDesa);
    if (appliedSls) params.set("sls", appliedSls);
    if (appliedSubsls) params.set("subsls", appliedSubsls);
    if (appliedMatchStatus) params.set("matchStatus", appliedMatchStatus);
    if (appliedSearch) params.set("search", appliedSearch);
    return params;
  }

  async function loadViewport() {
    const seq = ++requestSeq;
    if (inFlightController) inFlightController.abort();
    const controller = new AbortController();
    inFlightController = controller;

    const b = map.getBounds();
    const params = filterParams();
    params.set("minLat", b.getSouth());
    params.set("maxLat", b.getNorth());
    params.set("minLon", b.getWest());
    params.set("maxLon", b.getEast());
    params.set("zoom", String(map.getZoom()));

    setLoading(true);
    try {
      const resp = await fetchWithRetry(`/api/match-points?${params}`, controller.signal);
      if (seq !== requestSeq) return; // superseded by a newer viewport
      render(resp);
    } catch (err) {
      if (err.name === "AbortError") return;
      console.error("failed to load match points", err);
      statsEl.textContent = "Gagal memuat data. Mencoba lagi saat peta digeser…";
    } finally {
      if (seq === requestSeq) setLoading(false);
    }
  }

  function setLoading(v) {
    loadingEl.classList.toggle("hidden", !v);
  }

  // --- rendering ---------------------------------------------------------

  function render(resp) {
    layer.clearLayers();

    if (resp.type === "points") {
      for (const p of resp.points || []) {
        const marker = L.circleMarker([p.lat, p.lon], {
          renderer: canvasRenderer,
          radius: 5,
          color: "#fff",
          weight: 1,
          fillColor: "#7b4fbf",
          fillOpacity: 0.9,
        });
        marker.bindTooltip(tooltipHTML(p), { sticky: true, direction: "top" });
        marker.addTo(layer);
      }
    } else {
      for (const c of resp.clusters || []) {
        const size = clusterSize(c.count);
        const icon = L.divIcon({
          html: `<div class="cluster-icon" style="width:${size}px;height:${size}px;font-size:${fontSize(size)}px">${formatCount(c.count)}</div>`,
          className: "",
          iconSize: [size, size],
        });
        const marker = L.marker([c.lat, c.lon], { icon });
        marker.on("click", () => map.setView([c.lat, c.lon], Math.min(map.getZoom() + 3, 19)));
        marker.bindTooltip(`${formatCount(c.count)} titik di area ini — klik untuk perbesar`, { direction: "top" });
        marker.addTo(layer);
      }
    }

    updateStats(resp);
  }

  function clusterSize(count) {
    return Math.min(60, 20 + Math.log2(count + 1) * 5);
  }
  function fontSize(size) {
    return Math.max(10, Math.round(size * 0.32));
  }
  function formatCount(n) {
    if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + "jt";
    if (n >= 1_000) return (n / 1_000).toFixed(1) + "rb";
    return String(n);
  }

  function updateStats(resp) {
    const shown = resp.type === "points" ? (resp.points || []).length : (resp.clusters || []).length;
    const kind = resp.type === "points" ? "titik individual" : "kelompok";
    const lines = [];
    if (appliedKabkota) {
      const info = kabkotaByCode.get(appliedKabkota);
      let line = `Filter: ${info ? info.name : appliedKabkota}`;
      if (appliedKecamatan) line += ` / Kec. ${appliedKecamatan}`;
      if (appliedDesa) line += ` / Desa/Kel. ${appliedDesa}`;
      if (appliedSls) line += ` / SLS ${appliedSls}`;
      if (appliedSubsls) line += ` / SubSLS ${appliedSubsls}`;
      lines.push(line);
    }
    if (appliedMatchStatus) lines.push(`Match Status: ${appliedMatchStatus}`);
    lines.push(`Di area ini: ${resp.total.toLocaleString("id-ID")} titik`);
    lines.push(`Ditampilkan: ${shown.toLocaleString("id-ID")} ${kind}`);
    statsEl.textContent = lines.join("\n");
  }

  // --- wilayah filter ----------------------------------------------------
  // Reuses the Peta/Daftar cascade endpoints — same level_6_full_code, and
  // every wilayah here also exists in the points table.

  const kecamatanLevel = makeCascadingLevel({
    selectEl: kecamatanSelect, placeholder: "Semua Kecamatan", byCode: kecamatanByCode, labelPrefix: "Kec. ",
  });
  const desaLevel = makeCascadingLevel({
    selectEl: desaSelect, placeholder: "Semua Desa/Kelurahan", byCode: desaByCode, labelPrefix: "Desa/Kel. ",
  });
  const slsLevel = makeCascadingLevel({
    selectEl: slsSelect, placeholder: "Semua SLS", byCode: slsByCode, labelPrefix: "SLS ",
  });
  const subslsLevel = makeCascadingLevel({
    selectEl: subslsSelect, placeholder: "Semua SubSLS", byCode: subslsByCode, labelPrefix: "SubSLS ",
  });

  async function loadKabKotaOptions() {
    try {
      const list = await fetchWithRetry("/api/kabkota", undefined);
      for (const k of list) {
        kabkotaByCode.set(k.code, k);
        const opt = document.createElement("option");
        opt.value = k.code;
        opt.textContent = `${k.name} (${k.total.toLocaleString("id-ID")} titik)`;
        kabkotaSelect.appendChild(opt);
      }
    } catch (err) {
      console.error("failed to load kabkota list", err);
    }
  }

  async function loadFilterOptions() {
    try {
      const opts = await fetchWithRetry("/api/reg2022/filter-options", undefined);
      for (const v of opts.match_status || []) {
        const o = document.createElement("option");
        o.value = v;
        o.textContent = v;
        matchStatusSelect.appendChild(o);
      }
      const blank = document.createElement("option");
      blank.value = EMPTY_VALUE;
      blank.textContent = "(Kosong)";
      matchStatusSelect.appendChild(blank);
    } catch (err) {
      console.error("failed to load match filter options", err);
    }
  }

  kabkotaSelect.addEventListener("change", async () => {
    const kabkota = kabkotaSelect.value;
    desaLevel.reset();
    desaSelect.disabled = true;
    slsLevel.reset();
    slsSelect.disabled = true;
    subslsLevel.reset();
    subslsSelect.disabled = true;
    await kecamatanLevel.load(kabkota ? `/api/kecamatan?kabkota=${encodeURIComponent(kabkota)}` : null);
  });

  kecamatanSelect.addEventListener("change", async () => {
    const kabkota = kabkotaSelect.value;
    const kecamatan = kecamatanSelect.value;
    slsLevel.reset();
    slsSelect.disabled = true;
    subslsLevel.reset();
    subslsSelect.disabled = true;
    await desaLevel.load(
      kecamatan ? `/api/desa?kabkota=${encodeURIComponent(kabkota)}&kecamatan=${encodeURIComponent(kecamatan)}` : null
    );
  });

  desaSelect.addEventListener("change", async () => {
    const kabkota = kabkotaSelect.value;
    const kecamatan = kecamatanSelect.value;
    const desa = desaSelect.value;
    subslsLevel.reset();
    subslsSelect.disabled = true;
    await slsLevel.load(
      desa
        ? `/api/sls?kabkota=${encodeURIComponent(kabkota)}&kecamatan=${encodeURIComponent(kecamatan)}&desa=${encodeURIComponent(desa)}`
        : null
    );
  });

  slsSelect.addEventListener("change", async () => {
    const kabkota = kabkotaSelect.value;
    const kecamatan = kecamatanSelect.value;
    const desa = desaSelect.value;
    const sls = slsSelect.value;
    await subslsLevel.load(
      sls
        ? `/api/subsls?kabkota=${encodeURIComponent(kabkota)}&kecamatan=${encodeURIComponent(kecamatan)}&desa=${encodeURIComponent(desa)}&sls=${encodeURIComponent(sls)}`
        : null
    );
  });

  searchInput.addEventListener("keydown", (e) => {
    if (e.key === "Enter") applyFilters();
  });

  // Fits the map to whatever the filter now covers. Unlike the main map,
  // which has fixed per-wilayah boxes cached from startup, this extent
  // depends on the match_status/search filters too, so it's asked for per
  // apply.
  async function fitToFilter() {
    try {
      const b = await fetchWithRetry(`/api/match-bounds?${filterParams()}`, undefined);
      if (!b.total || !isFinite(b.min_lat) || !isFinite(b.max_lat)) return;
      map.invalidateSize();
      map.fitBounds([[b.min_lat, b.min_lon], [b.max_lat, b.max_lon]], { padding: [20, 20], animate: false });
    } catch (err) {
      console.error("failed to load match bounds", err);
    }
  }

  async function applyFilters() {
    appliedKabkota = kabkotaSelect.value;
    appliedKecamatan = kecamatanSelect.value;
    appliedDesa = desaSelect.value;
    appliedSls = slsSelect.value;
    appliedSubsls = subslsSelect.value;
    appliedMatchStatus = matchStatusSelect.value;
    appliedSearch = searchInput.value.trim();

    await fitToFilter();
    // fitBounds fires moveend -> scheduleLoad, but force one anyway in case
    // the view didn't actually move.
    scheduleLoad();
  }

  applyFilterBtn.addEventListener("click", applyFilters);

  // --- boot --------------------------------------------------------------

  let booted = false;
  document.addEventListener("view:petamatch-shown", async () => {
    if (booted) return;
    booted = true;
    loadKabKotaOptions();
    loadFilterOptions();
    await fitToFilter();
    map.on("moveend zoomend", scheduleLoad);
    loadViewport();
  });
})();
