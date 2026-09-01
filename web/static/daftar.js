(() => {
  "use strict";

  const { fetchWithRetry, makeCascadingLevel, esc } = App;

  const kabkotaSelect = document.getElementById("kabkota-select-list");
  const kecamatanSelect = document.getElementById("kecamatan-select-list");
  const desaSelect = document.getElementById("desa-select-list");
  const slsSelect = document.getElementById("sls-select-list");
  const subslsSelect = document.getElementById("subsls-select-list");
  const jenisPrelistSelect = document.getElementById("jenisprelist-select-list");
  const keberadaanKeluargaSelect = document.getElementById("keberadaankeluarga-select-list");
  const statusSelect = document.getElementById("status-select-list");
  const searchInput = document.getElementById("daftar-search");
  const tbody = document.getElementById("daftar-tbody");
  const loadingEl = document.getElementById("daftar-loading");
  const errorEl = document.getElementById("daftar-error");
  const pageInfoEl = document.getElementById("daftar-page-info");
  const prevBtn = document.getElementById("daftar-prev");
  const nextBtn = document.getElementById("daftar-next");
  const pageSizeSelect = document.getElementById("daftar-pagesize");
  const sortHeaders = document.querySelectorAll("#daftar-table th.sortable");
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
  let selectedJenisPrelist = "";
  let selectedKeberadaanKeluarga = "";
  let selectedStatus = "";
  let selectedSearch = "";

  // Must match points.EmptyValue on the server — the sentinel a dropdown
  // sends to mean "filter for rows where this column is blank", as opposed
  // to "" which means the filter itself isn't applied.
  const EMPTY_VALUE = "__EMPTY__";

  let currentPage = 1;
  let pageSize = 50;
  let sortColumn = "nama";
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

  // Wilayah + attribute filter params — shared by the table page fetch and
  // the PDF download link, so the download always reflects whatever's
  // currently filtered on screen.
  function buildFilterParams() {
    const params = new URLSearchParams();
    if (selectedKabkota) params.set("kabkota", selectedKabkota);
    if (selectedKecamatan) params.set("kecamatan", selectedKecamatan);
    if (selectedDesa) params.set("desa", selectedDesa);
    if (selectedSls) params.set("sls", selectedSls);
    if (selectedSubsls) params.set("subsls", selectedSubsls);
    if (selectedJenisPrelist) params.set("jenisPrelist", selectedJenisPrelist);
    if (selectedKeberadaanKeluarga) params.set("keberadaanKeluarga", selectedKeberadaanKeluarga);
    if (selectedStatus) params.set("status", selectedStatus);
    if (selectedSearch) params.set("search", selectedSearch);
    return params;
  }

  function buildParams() {
    const params = buildFilterParams();
    params.set("page", String(currentPage));
    params.set("pageSize", String(pageSize));
    params.set("sortBy", sortColumn);
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

    updateSortHeaderUI();
  }

  // Reflects sortColumn/sortDir onto the <th> elements: only the active
  // column gets the "sort-active" class (see .sort-arrow CSS), and its
  // data-dir picks the arrow direction.
  function updateSortHeaderUI() {
    for (const th of sortHeaders) {
      const active = th.dataset.sort === sortColumn;
      th.classList.toggle("sort-active", active);
      th.dataset.dir = active ? sortDir : "";
    }
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
  // Clicking a column header sorts by it; clicking the already-active
  // column just flips its direction, mirroring the old nama-only behavior
  // but generalized to every sortable column.
  for (const th of sortHeaders) {
    th.addEventListener("click", () => {
      const col = th.dataset.sort;
      if (col === sortColumn) {
        sortDir = sortDir === "asc" ? "desc" : "asc";
      } else {
        sortColumn = col;
        sortDir = "asc";
      }
      currentPage = 1;
      loadPage();
    });
  }

  // Debounced free-text name search — waits for a pause in typing so it
  // doesn't fire a query per keystroke.
  let searchDebounce = null;
  searchInput.addEventListener("input", () => {
    clearTimeout(searchDebounce);
    searchDebounce = setTimeout(() => {
      selectedSearch = searchInput.value.trim();
      currentPage = 1;
      loadPage();
    }, 350);
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

  // --- attribute filters (jenis prelist, keberadaan keluarga, status) ---
  // Independent of the wilayah cascade and of each other — no parent/child
  // resetting needed, just a value change and a reload.

  function fillAttrSelect(selectEl, values) {
    for (const v of values) {
      const opt = document.createElement("option");
      opt.value = v;
      opt.textContent = v;
      selectEl.appendChild(opt);
    }
    const blank = document.createElement("option");
    blank.value = EMPTY_VALUE;
    blank.textContent = "(Kosong)";
    selectEl.appendChild(blank);
  }

  async function loadFilterOptions() {
    try {
      const opts = await fetchWithRetry("/api/filter-options", undefined);
      fillAttrSelect(jenisPrelistSelect, opts.jenis_prelist || []);
      fillAttrSelect(keberadaanKeluargaSelect, opts.keberadaan_keluarga || []);
      fillAttrSelect(statusSelect, opts.status || []);
    } catch (err) {
      console.error("failed to load filter options", err);
    }
  }

  jenisPrelistSelect.addEventListener("change", () => {
    selectedJenisPrelist = jenisPrelistSelect.value;
    currentPage = 1;
    loadPage();
  });
  keberadaanKeluargaSelect.addEventListener("change", () => {
    selectedKeberadaanKeluarga = keberadaanKeluargaSelect.value;
    currentPage = 1;
    loadPage();
  });
  statusSelect.addEventListener("change", () => {
    selectedStatus = statusSelect.value;
    currentPage = 1;
    loadPage();
  });

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
    params.set("sortBy", sortColumn);
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
    updateSortHeaderUI();
    loadKabKotaOptions();
    loadFilterOptions();
    loadPage();
  });
})();
