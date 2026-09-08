(() => {
  "use strict";

  const { fetchWithRetry, makeCascadingLevel, makeMultiSelect, esc } = App;

  const kabkotaSelect = document.getElementById("kabkota-select-list");
  const kecamatanSelect = document.getElementById("kecamatan-select-list");
  const desaSelect = document.getElementById("desa-select-list");
  const slsSelect = document.getElementById("sls-select-list");
  const subslsSelect = document.getElementById("subsls-select-list");
  const jenisPrelistMS = makeMultiSelect(document.getElementById("jenisprelist-ms-list"));
  const keberadaanKeluargaMS = makeMultiSelect(document.getElementById("keberadaankeluarga-ms-list"));
  const statusMS = makeMultiSelect(document.getElementById("status-ms-list"));
  const penggunaanMS = makeMultiSelect(document.getElementById("penggunaan-ms-list"));
  const keberadaanBkuMS = makeMultiSelect(document.getElementById("keberadaanbku-ms-list"));
  const flagBaruSelect = document.getElementById("flagbaru-select-list");
  const flagRegsosekSelect = document.getElementById("flagregsosek-select-list");
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
  const updatedEl = document.getElementById("daftar-updated");

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
  // Arrays now: these three filters are multi-select, and every picked
  // value goes out as its own repeated query parameter.
  let appliedJenisPrelist = [];
  let appliedKeberadaanKeluarga = [];
  let appliedStatus = [];
  let appliedPenggunaan = [];
  let appliedKeberadaanBku = [];
  let appliedFlagBaru = "";
  let appliedFlagRegsosek = "";
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
        <td>${esc(p.penggunaan_bangunan)}</td>
        <td class="num">${esc(p.nomor_bangunan)}</td>
        <td>${esc(p.keberadaan_keluarga)}</td>
        <td>${esc(p.keberadaan_bku)}</td>
        <td><span class="status-dot" style="background:${color}"></span>${esc(p.status)}</td>
        <td class="mono">${esc(p.assignment_id)}</td>
        <td class="col-joined col-flag">${flagCell(p.ada_assignment_baru)}</td>
        <td class="col-joined mono">${esc(p.assignment_id_baru || "-")}</td>
        <td class="col-joined col-flag">${flagCell(p.ada_regsosek)}</td>
      </tr>`;
  }

  // A checkmark for found, an en dash for not — the title attribute
  // carries the meaning for anyone hovering or using a screen reader,
  // since the glyph alone doesn't say which table was matched.
  function flagCell(ada) {
    return ada
      ? `<span class="check-yes" title="Ditemukan">&#10003;</span>`
      : `<span class="check-no" title="Tidak ditemukan">&#8211;</span>`;
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
    // append, not set: one parameter per picked value, which is what
    // the server reads with q["status"] and turns into a SQL IN list.
    for (const v of appliedJenisPrelist) params.append("jenisPrelist", v);
    for (const v of appliedKeberadaanKeluarga) params.append("keberadaanKeluarga", v);
    for (const v of appliedStatus) params.append("status", v);
    for (const v of appliedPenggunaan) params.append("penggunaanBangunan", v);
    for (const v of appliedKeberadaanBku) params.append("keberadaanBku", v);
    if (appliedFlagBaru) params.set("flagBaru", appliedFlagBaru);
    if (appliedFlagRegsosek) params.set("flagRegsosek", appliedFlagRegsosek);
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
      : `<tr><td colspan="14" class="empty-row">Tidak ada data yang cocok dengan filter ini.</td></tr>`;

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
    selectEl: kecamatanSelect, placeholder: "Semua Kecamatan", byCode: kecamatanByCode, labelPrefix: "Kec. ", countNoun: "data",
  });
  const desaLevel = makeCascadingLevel({
    selectEl: desaSelect, placeholder: "Semua Desa/Kelurahan", byCode: desaByCode, labelPrefix: "Desa/Kel. ", countNoun: "data",
  });
  const slsLevel = makeCascadingLevel({
    selectEl: slsSelect, placeholder: "Semua SLS", byCode: slsByCode, labelPrefix: "SLS ", keepCode: true, countNoun: "data",
  });
  const subslsLevel = makeCascadingLevel({
    selectEl: subslsSelect, placeholder: "Semua SubSLS", byCode: subslsByCode, labelPrefix: "SubSLS ", countNoun: "data",
  });

  // The stamp is optional configuration, so anything other than a non-empty
  // string leaves the line hidden — a blank or half-rendered "Data terakhir
  // diperbarui:" would be worse than saying nothing.
  async function loadDataUpdatedAt() {
    try {
      const meta = await fetchWithRetry("/api/meta", undefined);
      if (!meta.data_updated_at) return;
      updatedEl.textContent = `Data terakhir diperbarui: ${meta.data_updated_at}`;
      updatedEl.classList.remove("hidden");
    } catch (err) {
      console.error("failed to load meta", err);
    }
  }

  async function loadKabKotaOptions() {
    try {
      const list = await fetchWithRetry("/api/kabkota", undefined);
      for (const k of list) {
        kabkotaByCode.set(k.code, k);
        const opt = document.createElement("option");
        opt.value = k.code;
        // "data", not "titik": this count is every row in the kabupaten/kota,
        // including rows with no usable coordinate. Same reasoning as
        // countNoun in makeCascadingLevel — the map menus keep "titik".
        opt.textContent = `${k.name} (${k.total.toLocaleString("id-ID")} data)`;
        kabkotaSelect.appendChild(opt);
      }
    } catch (err) {
      console.error("failed to load kabkota list", err);
    }
  }

  // --- attribute filters (jenis prelist, keberadaan keluarga, status) ---
  // Independent of the wilayah cascade and of each other — no parent/child
  // resetting needed, just a value change and a reload.

  async function loadFilterOptions() {
    try {
      const opts = await fetchWithRetry("/api/filter-options", undefined);
      jenisPrelistMS.setOptions(opts.jenis_prelist || []);
      keberadaanKeluargaMS.setOptions(opts.keberadaan_keluarga || []);
      statusMS.setOptions(opts.status || []);
      penggunaanMS.setOptions(opts.penggunaan_bangunan || []);
      keberadaanBkuMS.setOptions(opts.keberadaan_bku || []);
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
    appliedJenisPrelist = jenisPrelistMS.getValues();
    appliedKeberadaanKeluarga = keberadaanKeluargaMS.getValues();
    appliedStatus = statusMS.getValues();
    appliedPenggunaan = penggunaanMS.getValues();
    appliedKeberadaanBku = keberadaanBkuMS.getValues();
    appliedFlagBaru = flagBaruSelect.value;
    appliedFlagRegsosek = flagRegsosekSelect.value;
    appliedSearch = searchInput.value.trim();

    updateDownloadButtonState();
    currentPage = 1;
    loadPage();
  }

  applyFilterBtn.addEventListener("click", applyFilters);

  // --- filter panel collapse (phone only) --------------------------------
  // The filter controls stack to roughly a full screen on a phone,
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
  // The toggle is hidden above 600px (see .list-filter-toggle in
  // style.css), so a panel left collapsed at that width could never be
  // reopened. Track the breakpoint rather than reading it once at load:
  // the page can start narrow and then be widened (window resize, phone
  // rotation), and that used to strand the user with no filters and no
  // way to get them back.
  const narrowQuery = window.matchMedia("(max-width: 600px)");
  narrowQuery.addEventListener("change", (e) => setFiltersCollapsed(e.matches));
  setFiltersCollapsed(narrowQuery.matches);

  // Applying on a phone means you are done choosing — fold the panel away
  // so the results are what fills the screen.
  applyFilterBtn.addEventListener("click", () => {
    if (narrowQuery.matches) setFiltersCollapsed(true);
  });

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
    loadDataUpdatedAt();
    loadPage();
  });
})();
