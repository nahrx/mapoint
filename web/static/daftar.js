(() => {
  "use strict";

  const { fetchWithRetry, makeCascadingLevel, esc } = App;

  const kabkotaSelect = document.getElementById("kabkota-select-list");
  const kecamatanSelect = document.getElementById("kecamatan-select-list");
  const desaSelect = document.getElementById("desa-select-list");
  const slsSelect = document.getElementById("sls-select-list");
  const subslsSelect = document.getElementById("subsls-select-list");
  const tbody = document.getElementById("daftar-tbody");
  const loadingEl = document.getElementById("daftar-loading");
  const errorEl = document.getElementById("daftar-error");
  const pageInfoEl = document.getElementById("daftar-page-info");
  const prevBtn = document.getElementById("daftar-prev");
  const nextBtn = document.getElementById("daftar-next");
  const pageSizeSelect = document.getElementById("daftar-pagesize");
  const namaHeader = document.getElementById("daftar-sort-nama");
  const downloadBtn = document.getElementById("download-pdf-btn");

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

  let currentPage = 1;
  let pageSize = 50;
  let sortDir = "asc";
  let requestSeq = 0;

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

  function rowHTML(p, rowNo) {
    const color = STATUS_COLORS[p.status] || "#2a81cb";
    return `
      <tr>
        <td class="num">${rowNo}</td>
        <td>${esc(p.nama)}</td>
        <td class="addr" title="${esc(p.alamat)}">${esc(p.alamat)}</td>
        <td>${esc(p.subsls)}</td>
        <td>${esc(p.jenis_prelist)}</td>
        <td class="num">${esc(p.keberadaan_usaha)}</td>
        <td>${esc(p.keberadaan_keluarga)}</td>
        <td><span class="status-dot" style="background:${color}"></span>${esc(p.status)}</td>
        <td class="mono">${esc(p.assignment_id)}</td>
      </tr>`;
  }

  function setLoading(v) {
    loadingEl.classList.toggle("hidden", !v);
  }

  // Wilayah filter params only — shared by the table page fetch and the
  // PDF download link.
  function buildFilterParams() {
    const params = new URLSearchParams();
    if (selectedKabkota) params.set("kabkota", selectedKabkota);
    if (selectedKecamatan) params.set("kecamatan", selectedKecamatan);
    if (selectedDesa) params.set("desa", selectedDesa);
    if (selectedSls) params.set("sls", selectedSls);
    if (selectedSubsls) params.set("subsls", selectedSubsls);
    return params;
  }

  function buildParams() {
    const params = buildFilterParams();
    params.set("page", String(currentPage));
    params.set("pageSize", String(pageSize));
    params.set("dir", sortDir);
    return params;
  }

  // The download button only makes sense once the filter is pinned all
  // the way down to one SubSLS (see handleListPDF on the server) — that's
  // the only scope small enough for a printable report, and it's also
  // the point at which level_6_full_code is fully determined.
  function updateDownloadButtonState() {
    downloadBtn.disabled = !(selectedKabkota && selectedKecamatan && selectedDesa && selectedSls && selectedSubsls);
  }

  async function loadPage() {
    const seq = ++requestSeq;
    setLoading(true);
    errorEl.classList.add("hidden");
    try {
      const resp = await fetchWithRetry(`/api/list?${buildParams()}`, undefined);
      if (seq !== requestSeq) return; // superseded by a newer request
      render(resp);
    } catch (err) {
      if (seq !== requestSeq) return;
      console.error("failed to load daftar", err);
      errorEl.textContent = "Gagal memuat data. Coba muat ulang halaman.";
      errorEl.classList.remove("hidden");
    } finally {
      if (seq === requestSeq) setLoading(false);
    }
  }

  function render(resp) {
    const startRow = (resp.page - 1) * resp.page_size + 1;
    tbody.innerHTML = resp.items.length
      ? resp.items.map((p, i) => rowHTML(p, startRow + i)).join("")
      : `<tr><td colspan="9" class="empty-row">Tidak ada data yang cocok dengan filter ini.</td></tr>`;

    const totalPages = Math.max(1, Math.ceil(resp.total / resp.page_size));
    pageInfoEl.textContent = `Halaman ${resp.page.toLocaleString("id-ID")} dari ${totalPages.toLocaleString("id-ID")} (${resp.total.toLocaleString("id-ID")} data)`;
    prevBtn.disabled = resp.page <= 1;
    nextBtn.disabled = resp.page >= totalPages;

    namaHeader.dataset.dir = sortDir;
  }

  prevBtn.addEventListener("click", () => {
    if (currentPage > 1) {
      currentPage--;
      loadPage();
    }
  });
  nextBtn.addEventListener("click", () => {
    currentPage++;
    loadPage();
  });
  pageSizeSelect.addEventListener("change", () => {
    pageSize = parseInt(pageSizeSelect.value, 10) || 50;
    currentPage = 1;
    loadPage();
  });
  namaHeader.addEventListener("click", () => {
    sortDir = sortDir === "asc" ? "desc" : "asc";
    currentPage = 1;
    loadPage();
  });

  // --- wilayah filter (kabkota -> kecamatan -> desa -> sls) -------------

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
    updateDownloadButtonState();
    currentPage = 1;
    loadPage();
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
    updateDownloadButtonState();
    currentPage = 1;
    loadPage();
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
    updateDownloadButtonState();
    currentPage = 1;
    loadPage();
  });

  slsSelect.addEventListener("change", async () => {
    selectedSls = slsSelect.value;
    selectedSubsls = "";
    await subslsLevel.load(
      selectedSls
        ? `/api/subsls?kabkota=${encodeURIComponent(selectedKabkota)}&kecamatan=${encodeURIComponent(selectedKecamatan)}&desa=${encodeURIComponent(selectedDesa)}&sls=${encodeURIComponent(selectedSls)}`
        : null
    );
    updateDownloadButtonState();
    currentPage = 1;
    loadPage();
  });

  subslsSelect.addEventListener("change", () => {
    selectedSubsls = subslsSelect.value;
    updateDownloadButtonState();
    currentPage = 1;
    loadPage();
  });

  // --- PDF download ------------------------------------------------------

  downloadBtn.addEventListener("click", () => {
    if (downloadBtn.disabled) return;
    const params = buildFilterParams();
    params.set("dir", sortDir);
    const url = `/api/list/pdf?${params}`;
    // A plain navigation would work too (Content-Disposition: attachment
    // makes the browser download instead of leaving the page), but a
    // temporary link keeps this an explicit, self-contained action and
    // avoids ever touching location.href.
    const a = document.createElement("a");
    a.href = url;
    a.rel = "noopener";
    document.body.appendChild(a);
    a.click();
    a.remove();
  });

  // --- boot ------------------------------------------------------------

  // The Daftar view starts hidden, so its first real load is deferred
  // until nav.js shows it — no point racing the Peta view for the initial
  // ClickHouse queries.
  let booted = false;
  document.addEventListener("view:daftar-shown", () => {
    if (booted) return;
    booted = true;
    loadKabKotaOptions();
    loadPage();
  });
})();
