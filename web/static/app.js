(() => {
  "use strict";

  const statsEl = document.getElementById("stats");
  const loadingEl = document.getElementById("loading");
  const panelEl = document.getElementById("panel");
  const panelToggleBtn = document.getElementById("panel-toggle");
  const kabkotaSelect = document.getElementById("kabkota-select");
  const kecamatanSelect = document.getElementById("kecamatan-select");
  const desaSelect = document.getElementById("desa-select");
  const slsSelect = document.getElementById("sls-select");
  const subslsSelect = document.getElementById("subsls-select");
  const applyFilterBtn = document.getElementById("apply-filter-btn");

  const map = L.map("map", { preferCanvas: true, worldCopyJump: true }).setView([-2.5, 118], 5);

  // The Peta view can be hidden (display:none) while the Daftar tab is
  // active; Leaflet doesn't notice its container resizing back to full
  // size on its own, so nav.js dispatches this event right after showing
  // the view again.
  document.addEventListener("view:peta-shown", () => map.invalidateSize());

  // Collapse the filter panel down to just its title bar so it doesn't
  // cover the map on small screens or when the user just wants to look
  // around — the map itself is a fixed absolute layer underneath, so this
  // doesn't need an invalidateSize() call.
  panelToggleBtn.addEventListener("click", () => {
    const collapsed = panelEl.classList.toggle("collapsed");
    panelToggleBtn.setAttribute("aria-expanded", String(!collapsed));
    panelToggleBtn.title = collapsed ? "Tampilkan filter" : "Sembunyikan filter";
  });

  const osmLayer = L.tileLayer("https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png", {
    maxZoom: 19,
    attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors',
  });
  // Esri World Imagery: free satellite tiles, no API key/signup needed —
  // just attribution. Good default for a tool like this; swap for Mapbox
  // Satellite (needs a token, free up to 50k loads/month) or EOX
  // Sentinel-2 cloudless if a different look/quality is ever needed.
  const satelliteLayer = L.tileLayer(
    "https://server.arcgisonline.com/ArcGIS/rest/services/World_Imagery/MapServer/tile/{z}/{y}/{x}",
    {
      maxZoom: 19,
      attribution: "Tiles &copy; Esri &mdash; Source: Esri, Maxar, Earthstar Geographics, and the GIS User Community",
    }
  );
  // A light labels/roads overlay on top of satellite tiles so place names
  // and roads stay legible ("hybrid" view), toggled together with it below.
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
    { collapsed: false, position: "bottomleft" }
  ).addTo(map);

  const canvasRenderer = L.canvas({ padding: 0.5 });
  const layer = L.layerGroup().addTo(map);

  // The map is preferCanvas:true (for the point markers/clusters), so a
  // vector layer with no explicit renderer — like the SubSLS polygon below
  // — would default to canvas too. That's a problem for a purely
  // decorative overlay: a canvas is one opaque DOM element covering the
  // whole viewport, so `interactive: false` stops *Leaflet* from reacting
  // to it, but the element itself still physically sits on top and blocks
  // every mouse event (hover included) from ever reaching the point
  // markers underneath, regardless of that flag. SVG doesn't have this
  // problem — each shape is its own element, and a non-interactive one
  // gets `pointer-events: none` from Leaflet's own CSS, so events fall
  // straight through to whatever's beneath it. Shared/reused across
  // reloads the same way canvasRenderer is, so old polygon loads don't
  // leak renderer instances onto the map.
  const polygonRenderer = L.svg({ padding: 0.5 });

  // --- SubSLS boundary polygon --------------------------------------------
  // Only meaningful once the filter is pinned all the way down to one
  // SubSLS (same rule as the Daftar menu's PDF download) — a wilayah any
  // wider than that has no single boundary to draw. The backend serves
  // this from an optional PostGIS database; if it's not configured there,
  // the endpoint 503s and this just silently leaves the map without a
  // polygon rather than showing an error over something cosmetic.
  let polygonLayer = null;
  let polygonSeq = 0;

  function clearPolygon() {
    if (polygonLayer) {
      map.removeLayer(polygonLayer);
      polygonLayer = null;
    }
  }

  async function loadSubSlsPolygon() {
    const seq = ++polygonSeq;
    clearPolygon();
    if (!(appliedKabkota && appliedKecamatan && appliedDesa && appliedSls && appliedSubsls)) {
      return;
    }
    const params = new URLSearchParams({
      kabkota: appliedKabkota, kecamatan: appliedKecamatan, desa: appliedDesa,
      sls: appliedSls, subsls: appliedSubsls,
    });
    try {
      const geojson = await fetchWithRetry(`/api/subsls-polygon?${params}`, undefined);
      if (seq !== polygonSeq) return; // filter changed again while this was in flight
      if (!geojson.features || geojson.features.length === 0) return;
      polygonLayer = L.geoJSON(geojson, {
        style: { color: "#e63946", weight: 2, fillColor: "#e63946", fillOpacity: 0.08 },
        // Purely a visual boundary, not a clickable/hoverable shape — and
        // rendered via polygonRenderer (plain SVG) rather than the map's
        // default canvas, so it doesn't block mouse events meant for the
        // point markers underneath. See polygonRenderer above for why both
        // of these matter together.
        interactive: false,
        renderer: polygonRenderer,
      }).addTo(map);
    } catch (err) {
      if (seq !== polygonSeq) return;
      // Optional overlay — log and move on rather than surfacing an error
      // for something that isn't configured on every deployment.
      console.error("failed to load SubSLS polygon", err);
    }
  }

  let datasetTotal = null;
  let datasetBounds = null; // [[minLat,minLon],[maxLat,maxLon]], for the "Semua Kabupaten/Kota" refit
  let kabkotaByCode = new Map();
  let kecamatanByCode = new Map();
  let desaByCode = new Map();
  let slsByCode = new Map();
  let subslsByCode = new Map();

  // appliedKabkota..appliedSubsls are the wilayah filter actually in effect
  // for the map/polygon right now — only the "Terapkan Filter" button
  // (applyFilterBtn below) updates these, from whatever the selects
  // currently hold. The selects themselves are free to be changed (which
  // still cascades child dropdown options, e.g. picking a kabkota still
  // loads its kecamatan list right away) without touching what's actually
  // loaded on the map until that button is clicked.
  let appliedKabkota = "";
  let appliedKecamatan = "";
  let appliedDesa = "";
  let appliedSls = "";
  let appliedSubsls = "";

  // --- status labels -------------------------------------------------

  const STATUS_COLORS = {
    "OPEN": "#888888",
    "DRAFT": "#f0ad4e",
    "SUBMITTED BY Pencacah": "#5bc0de",
    "APPROVED BY Pengawas": "#2a9d3f",
    "REJECTED BY Pengawas": "#d9534f",
    "REVOKED BY Pengawas": "#d9534f",
    "SUBMITTED RESPONDENT": "#5bc0de",
    "EDITED BY Pengawas": "#5bc0de",
    "REJECTED BY Admin Kabupaten": "#d9534f",
    "EDITED BY Admin Kabupaten": "#5bc0de",
    "COMPLETED BY Admin Kabupaten": "#2a9d3f",
    "REVOKED BY Admin Kabupaten": "#d9534f",
  };
  function colorForStatus(status) {
    return STATUS_COLORS[status] || "#2a81cb";
  }

  const { fetchWithRetry, makeCascadingLevel, esc } = App;

  function tooltipHTML(p) {
    return `
      <div class="titik-tooltip">
        <div><b>Assignment ID:</b> ${esc(p.assignment_id)}</div>
        <div><b>Nama:</b> ${esc(p.nama)}</div>
        <div><b>Alamat:</b> ${esc(p.alamat)}</div>
        <div><b>ID SUBSLS:</b> ${esc(p.subsls)}</div>
        <div><b>Jenis Prelist:</b> ${esc(p.jenis_prelist)}</div>
        <div><b>Keberadaan Usaha:</b> ${esc(p.keberadaan_usaha)}</div>
        <div><b>Keberadaan Keluarga:</b> ${esc(p.keberadaan_keluarga)}</div>
        <div><b>Status:</b> ${esc(p.status)}</div>
      </div>`;
  }

  // --- fetching --------------------------------------------------------

  let inFlightController = null;
  let debounceTimer = null;
  let requestSeq = 0;

  function scheduleLoad() {
    clearTimeout(debounceTimer);
    debounceTimer = setTimeout(loadViewport, 250);
  }

  async function loadViewport() {
    const seq = ++requestSeq;

    if (inFlightController) inFlightController.abort();
    const controller = new AbortController();
    inFlightController = controller;

    const b = map.getBounds();
    const zoom = map.getZoom();
    const params = new URLSearchParams({
      minLat: b.getSouth(), maxLat: b.getNorth(),
      minLon: b.getWest(), maxLon: b.getEast(),
      zoom: String(zoom),
    });
    if (appliedKabkota) params.set("kabkota", appliedKabkota);
    if (appliedKecamatan) params.set("kecamatan", appliedKecamatan);
    if (appliedDesa) params.set("desa", appliedDesa);
    if (appliedSls) params.set("sls", appliedSls);
    if (appliedSubsls) params.set("subsls", appliedSubsls);

    setLoading(true);
    try {
      const resp = await fetchWithRetry(`/api/points?${params}`, controller.signal);
      if (seq !== requestSeq) return; // superseded by a newer viewport
      render(resp);
    } catch (err) {
      if (err.name === "AbortError") return;
      console.error("failed to load points", err);
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
          fillColor: colorForStatus(p.status),
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
        marker.on("click", () => {
          map.setView([c.lat, c.lon], Math.min(map.getZoom() + 3, 19));
        });
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
    if (datasetTotal !== null) lines.push(`Total data: ${datasetTotal.toLocaleString("id-ID")} titik`);
    if (appliedKabkota) {
      const info = kabkotaByCode.get(appliedKabkota);
      let line = `Filter: ${info ? info.name : appliedKabkota}`;
      if (appliedKecamatan) line += ` / Kec. ${appliedKecamatan}`;
      if (appliedDesa) line += ` / Desa/Kel. ${appliedDesa}`;
      if (appliedSls) line += ` / SLS ${appliedSls}`;
      if (appliedSubsls) line += ` / SubSLS ${appliedSubsls}`;
      lines.push(line);
    }
    lines.push(`Di area ini: ${resp.total.toLocaleString("id-ID")} titik`);
    lines.push(`Ditampilkan: ${shown.toLocaleString("id-ID")} ${kind}`);
    statsEl.textContent = lines.join("\n");
  }

  // --- boot ----------------------------------------------------------

  function fitTo(bounds) {
    if (!bounds) return;
    map.invalidateSize();
    map.fitBounds(bounds, { padding: [20, 20], animate: false });
  }

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

  function boundsOf(info) {
    if (info && isFinite(info.min_lat) && isFinite(info.max_lat) && isFinite(info.min_lon) && isFinite(info.max_lon)) {
      return [[info.min_lat, info.min_lon], [info.max_lat, info.max_lon]];
    }
    return null;
  }

  // Picks the bbox of the deepest wilayah level currently applied, falling
  // back up the chain (SubSLS -> SLS -> desa -> kecamatan -> kabkota ->
  // whole dataset) to whichever ancestor actually has a usable bbox.
  // Replaces what used to be five near-identical per-select fallback
  // chains, now that there's a single moment (the Filter button) where the
  // map re-zooms to match the filter, instead of one per select change.
  function currentBounds() {
    const candidates = [
      boundsOf(subslsByCode.get(appliedSubsls)),
      boundsOf(slsByCode.get(appliedSls)),
      boundsOf(desaByCode.get(appliedDesa)),
      boundsOf(kecamatanByCode.get(appliedKecamatan)),
      boundsOf(kabkotaByCode.get(appliedKabkota)),
      datasetBounds,
    ];
    return candidates.find(Boolean);
  }

  // The five selects below only cascade each other's *options* — picking a
  // kabkota loads its kecamatan list right away, same as before — but no
  // longer touch the map (no re-zoom, no point/polygon reload) on their
  // own. That only happens once "Terapkan Filter" is clicked (see
  // applyFilterBtn below), so switching through several levels while
  // deciding doesn't fire a request per click.

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

  // subslsSelect has no child level to cascade — nothing to do here at all
  // now; its value is only read when Terapkan Filter is clicked.

  applyFilterBtn.addEventListener("click", () => {
    appliedKabkota = kabkotaSelect.value;
    appliedKecamatan = kecamatanSelect.value;
    appliedDesa = desaSelect.value;
    appliedSls = slsSelect.value;
    appliedSubsls = subslsSelect.value;

    fitTo(currentBounds());
    // fitBounds triggers moveend -> scheduleLoad already; force an
    // immediate reload too in case the view didn't actually move.
    scheduleLoad();
    loadSubSlsPolygon();
  });

  async function boot() {
    await loadKabKotaOptions();
    try {
      const b = await fetchWithRetry("/api/bounds", undefined);
      datasetTotal = b.total;
      if (isFinite(b.min_lat) && isFinite(b.max_lat) && isFinite(b.min_lon) && isFinite(b.max_lon) && b.total > 0) {
        datasetBounds = [[b.min_lat, b.min_lon], [b.max_lat, b.max_lon]];
        fitTo(datasetBounds);
      }
    } catch (err) {
      console.error("failed to load bounds", err);
    }
    map.on("moveend zoomend", scheduleLoad);
    loadViewport();
  }

  boot();
})();
