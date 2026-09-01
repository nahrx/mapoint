(() => {
  "use strict";

  const statsEl = document.getElementById("stats");
  const loadingEl = document.getElementById("loading");
  const kabkotaSelect = document.getElementById("kabkota-select");
  const kecamatanSelect = document.getElementById("kecamatan-select");
  const desaSelect = document.getElementById("desa-select");
  const slsSelect = document.getElementById("sls-select");
  const subslsSelect = document.getElementById("subsls-select");

  const map = L.map("map", { preferCanvas: true, worldCopyJump: true }).setView([-2.5, 118], 5);

  // The Peta view can be hidden (display:none) while the Daftar tab is
  // active; Leaflet doesn't notice its container resizing back to full
  // size on its own, so nav.js dispatches this event right after showing
  // the view again.
  document.addEventListener("view:peta-shown", () => map.invalidateSize());

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

  let datasetTotal = null;
  let datasetBounds = null; // [[minLat,minLon],[maxLat,maxLon]], for the "Semua Kabupaten/Kota" refit
  let kabkotaByCode = new Map();
  let selectedKabkota = "";
  let kecamatanByCode = new Map();
  let selectedKecamatan = "";
  let desaByCode = new Map();
  let selectedDesa = "";
  let slsByCode = new Map();
  let selectedSls = "";
  let subslsByCode = new Map();
  let selectedSubsls = "";

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
    if (selectedKabkota) params.set("kabkota", selectedKabkota);
    if (selectedKecamatan) params.set("kecamatan", selectedKecamatan);
    if (selectedDesa) params.set("desa", selectedDesa);
    if (selectedSls) params.set("sls", selectedSls);
    if (selectedSubsls) params.set("subsls", selectedSubsls);

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
    if (selectedKabkota) {
      const info = kabkotaByCode.get(selectedKabkota);
      let line = `Filter: ${info ? info.name : selectedKabkota}`;
      if (selectedKecamatan) line += ` / Kec. ${selectedKecamatan}`;
      if (selectedDesa) line += ` / Desa/Kel. ${selectedDesa}`;
      if (selectedSls) line += ` / SLS ${selectedSls}`;
      if (selectedSubsls) line += ` / SubSLS ${selectedSubsls}`;
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

  // Ancestor bounds to fall back to when the selected level itself has no
  // usable bbox, nearest first.
  function ancestorBounds() {
    return [
      boundsOf(kecamatanByCode.get(selectedKecamatan)),
      boundsOf(kabkotaByCode.get(selectedKabkota)),
      datasetBounds,
    ];
  }

  kabkotaSelect.addEventListener("change", async () => {
    selectedKabkota = kabkotaSelect.value;
    selectedKecamatan = "";
    selectedDesa = "";
    selectedSls = "";
    selectedSubsls = "";
    desaLevel.reset();
    desaSelect.disabled = true;
    slsLevel.reset();
    slsSelect.disabled = true;
    subslsLevel.reset();
    subslsSelect.disabled = true;
    await kecamatanLevel.load(selectedKabkota ? `/api/kecamatan?kabkota=${encodeURIComponent(selectedKabkota)}` : null);

    fitTo(boundsOf(kabkotaByCode.get(selectedKabkota)) || datasetBounds);
    // fitBounds triggers moveend -> scheduleLoad already; force an
    // immediate reload too in case the view didn't actually move.
    scheduleLoad();
  });

  kecamatanSelect.addEventListener("change", async () => {
    selectedKecamatan = kecamatanSelect.value;
    selectedDesa = "";
    selectedSls = "";
    selectedSubsls = "";
    slsLevel.reset();
    slsSelect.disabled = true;
    subslsLevel.reset();
    subslsSelect.disabled = true;
    await desaLevel.load(
      selectedKecamatan
        ? `/api/desa?kabkota=${encodeURIComponent(selectedKabkota)}&kecamatan=${encodeURIComponent(selectedKecamatan)}`
        : null
    );

    fitTo(boundsOf(kecamatanByCode.get(selectedKecamatan)) || boundsOf(kabkotaByCode.get(selectedKabkota)) || datasetBounds);
    scheduleLoad();
  });

  desaSelect.addEventListener("change", async () => {
    selectedDesa = desaSelect.value;
    selectedSls = "";
    selectedSubsls = "";
    subslsLevel.reset();
    subslsSelect.disabled = true;
    await slsLevel.load(
      selectedDesa
        ? `/api/sls?kabkota=${encodeURIComponent(selectedKabkota)}&kecamatan=${encodeURIComponent(selectedKecamatan)}&desa=${encodeURIComponent(selectedDesa)}`
        : null
    );

    fitTo(boundsOf(desaByCode.get(selectedDesa)) || ancestorBounds().find(Boolean) || datasetBounds);
    scheduleLoad();
  });

  slsSelect.addEventListener("change", async () => {
    selectedSls = slsSelect.value;
    selectedSubsls = "";
    await subslsLevel.load(
      selectedSls
        ? `/api/subsls?kabkota=${encodeURIComponent(selectedKabkota)}&kecamatan=${encodeURIComponent(selectedKecamatan)}&desa=${encodeURIComponent(selectedDesa)}&sls=${encodeURIComponent(selectedSls)}`
        : null
    );

    const fallbacks = [boundsOf(desaByCode.get(selectedDesa)), ...ancestorBounds()];
    fitTo(boundsOf(slsByCode.get(selectedSls)) || fallbacks.find(Boolean) || datasetBounds);
    scheduleLoad();
  });

  subslsSelect.addEventListener("change", () => {
    selectedSubsls = subslsSelect.value;
    const fallbacks = [boundsOf(slsByCode.get(selectedSls)), boundsOf(desaByCode.get(selectedDesa)), ...ancestorBounds()];
    fitTo(boundsOf(subslsByCode.get(selectedSubsls)) || fallbacks.find(Boolean) || datasetBounds);
    scheduleLoad();
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
