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
  const downloadPdfBtn = document.getElementById("download-pdf-btn");
  const downloadXlsxBtn = document.getElementById("download-xlsx-btn");
  const applyFilterBtn = document.getElementById("apply-filter-btn-list");
  const filtersEl = document.getElementById("daftar-filters");
  const filterToggleBtn = document.getElementById("daftar-filter-toggle");

  let kabkotaByCode = new Map();
  let kecamatanByCode = new Map();
  let desaByCode = new Map();
  let slsByCode = new Map();
  let subslsByCode = new Map();

  // applied* are the filters actually in effect for the table/downloads
  // right now — only applyFilterBtn's click handler (see below) updates
  // these, reading whatever the selects/search box currently hold at that
  // moment. Changing a select or typing in the search box never touches
  // these on its own (wilayah selects still cascade their *own* child
  // dropdown options live, same as before — see the change handlers
  // further down — just without reloading the table).
  let appliedKabkota = "";
  let appliedKecamatan = "";
  let appliedDesa = "";
  let appliedSls = "";
  let appliedSubsls = "";
  let appliedJenisPrelist = "";
  let appliedKeberadaanKeluarga = "";
  let appliedStatus = "";
  let appliedSearch = "";

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
        <td class="num">${esc(p.nomor_bangunan)}</td>
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
    if (appliedKabkota) params.set("kabkota", appliedKabkota);
    if (appliedKecamatan) params.set("kecamatan", appliedKecamatan);
    if (appliedDesa) params.set("desa", appliedDesa);
    if (appliedSls) params.set("sls", appliedSls);
    if (appliedSubsls) params.set("subsls", appliedSubsls);
    if (appliedJenisPrelist) params.set("jenisPrelist", appliedJenisPrelist);
    if (appliedKeberadaanKeluarga) params.set("keberadaanKeluarga", appliedKeberadaanKeluarga);
    if (appliedStatus) params.set("status", appliedStatus);
    if (appliedSearch) params.set("search", appliedSearch);
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

  // Both download buttons need the filter pinned at least down to one
  // desa/kelurahan (see prepareReport on the server) — anything wider runs
  // to hundreds of thousands of rows. Narrowing further to an SLS or
  // SubSLS is allowed and just makes the report smaller.
  function updateDownloadButtonState() {
    const ready = !!(appliedKabkota && appliedKecamatan && appliedDesa);
    downloadPdfBtn.disabled = !ready;
    downloadXlsxBtn.disabled = !ready;
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

  // Search is just another filter now — typing doesn't reload anything on
  // its own, only Enter (as a shortcut) or Terapkan Filter does.
  searchInput.addEventListener("keydown", (e) => {
    if (e.key === "Enter") applyFilters();
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

  // No listeners needed on the attribute selects themselves — their value
  // is only read when Terapkan Filter is clicked (see applyFilters below).

  // The four selects below only cascade each other's *options* — picking a
  // kabkota loads its kecamatan list right away, same as before — without
  // touching the table on their own. Terapkan Filter (applyFilters) is the
  // only thing that actually reloads the table.

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

  // Reads every filter control's current value into applied*, then reloads
  // page 1 with them — the one place all of this actually takes effect.
  function applyFilters() {
    appliedKabkota = kabkotaSelect.value;
    appliedKecamatan = kecamatanSelect.value;
    appliedDesa = desaSelect.value;
    appliedSls = slsSelect.value;
    appliedSubsls = subslsSelect.value;
    appliedJenisPrelist = jenisPrelistSelect.value;
    appliedKeberadaanKeluarga = keberadaanKeluargaSelect.value;
    appliedStatus = statusSelect.value;
    appliedSearch = searchInput.value.trim();

    updateDownloadButtonState();
    currentPage = 1;
    loadPage();
  }

  applyFilterBtn.addEventListener("click", applyFilters);

  // --- filter panel collapse (phone only) --------------------------------
  // The nine filter controls stack to roughly a full screen on a phone,
  // which would push the table itself out of view. The toggle button is
  // hidden by CSS above 600px, where everything fits side by side anyway.

  function setFiltersCollapsed(collapsed) {
    filtersEl.classList.toggle("collapsed", collapsed);
    filterToggleBtn.setAttribute("aria-expanded", String(!collapsed));
  }

  filterToggleBtn.addEventListener("click", () => {
    setFiltersCollapsed(!filtersEl.classList.contains("collapsed"));
  });

  // Applying a filter on a phone means you're done choosing — fold the
  // panel away so the results you just asked for are what's on screen.
  if (window.matchMedia("(max-width: 600px)").matches) {
    setFiltersCollapsed(true);
    applyFilterBtn.addEventListener("click", () => setFiltersCollapsed(true));
  }

  // --- PDF / Excel download ----------------------------------------------

  // Shared by both download buttons — same filter/sort params, same
  // "build a temporary link and click it" approach (works for any
  // Content-Disposition: attachment endpoint, and keeps this an explicit,
  // self-contained action rather than ever touching location.href).
  function downloadReport(btn, apiPath) {
    if (btn.disabled) return;
    const params = buildFilterParams();
    params.set("sortBy", sortColumn);
    params.set("dir", sortDir);
    const url = `${apiPath}?${params}`;
    const a = document.createElement("a");
    a.href = url;
    a.rel = "noopener";
    document.body.appendChild(a);
    a.click();
    a.remove();
  }

  downloadPdfBtn.addEventListener("click", () => downloadReport(downloadPdfBtn, "/api/list/pdf"));
  downloadXlsxBtn.addEventListener("click", () => downloadReport(downloadXlsxBtn, "/api/list/xlsx"));

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
