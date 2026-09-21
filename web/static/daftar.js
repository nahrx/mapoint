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
  const nonResponSelect = document.getElementById("nonrespon-select-list");
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
  const downloadDialog = document.getElementById("download-dialog");
  const downloadDialogTitle = document.getElementById("download-dialog-title");
  const downloadDialogScope = document.getElementById("download-dialog-scope");
  const downloadDialogNote = document.getElementById("download-dialog-note");
  const downloadDialogCancel = document.getElementById("download-dialog-cancel");
  const downloadDialogConfirm = document.getElementById("download-dialog-confirm");
  const downloadDialogPassword = document.getElementById("download-dialog-password");
  const downloadPasswordInput = document.getElementById("download-password-input");
  const downloadDialogError = document.getElementById("download-dialog-error");
  const splitToggle = document.getElementById("split-subsls-toggle");
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
  let appliedNonRespon = "";
  let appliedSearch = "";

  // Must match points.EmptyValue on the server — the sentinel a dropdown
  // sends to mean "filter for rows where this column is blank", as opposed
  // to "" which means the filter itself isn't applied.
  const EMPTY_VALUE = "__EMPTY__";

  let currentPage = 1;
  let pageSize = 50;
  // Row count of the applied filter, from the last /api/list response —
  // shown in the download dialog so "one file for the whole scope" comes
  // with the number that says how big that file will be.
  let appliedTotal = 0;
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
        <td class="num">${esc(p.nomor_bangunan)}</td>
        <td>${esc(p.jenis_prelist)}</td>
        <td>${esc(p.penggunaan_bangunan)}</td>
        <td>${esc(p.keberadaan_keluarga)}</td>
        <td>${esc(p.keberadaan_bku)}</td>
        <td><span class="status-dot" style="background:${color}"></span>${esc(p.status)}</td>
        <td class="col-flag">${nonResponCell(p.non_respon)}</td>
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

  // Same glyphs, different meaning: checked when the row carries a BANR
  // (berita acara non-respon) reference in no_banr.
  function nonResponCell(yes) {
    return yes
      ? `<span class="check-yes" title="Non respon (ada BANR)">&#10003;</span>`
      : `<span class="check-no" title="Bukan non respon">&#8211;</span>`;
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
    if (appliedNonRespon) params.set("nonRespon", appliedNonRespon);
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
  // kabupaten/kota. The single report itself stops at desa/kelurahan (see
  // prepareReport on the server — a kecamatan runs to a hundred thousand
  // rows), but the download dialog they open can always fall back to the
  // per-SubSLS ZIP, which is fine at any width because each file is still
  // one SubSLS; a whole kabupaten/kota additionally asks for the second
  // password in the dialog. Narrowing further to an SLS or SubSLS just
  // makes the report smaller.
  function updateDownloadButtonState() {
    const ready = !!appliedKabkota;
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
      : `<tr><td colspan="15" class="empty-row">Tidak ada data yang cocok dengan filter ini.</td></tr>`;

    appliedTotal = resp.total;
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
    appliedNonRespon = nonResponSelect.value;
    appliedSearch = searchInput.value.trim();

    updateDownloadButtonState();
    currentPage = 1;
    loadPage();
  }

  applyFilterBtn.addEventListener("click", applyFilters);

  // --- preset filter (localStorage) --------------------------------------
  // The preset bar itself lives in common.js; these two functions are the
  // half only this menu can supply — reading its own controls, and putting
  // a saved filter back into them.

  function readFilterState() {
    return {
      kabkota: kabkotaSelect.value,
      kecamatan: kecamatanSelect.value,
      desa: desaSelect.value,
      sls: slsSelect.value,
      subsls: subslsSelect.value,
      search: searchInput.value.trim(),
      jenisPrelist: jenisPrelistMS.getValues(),
      penggunaanBangunan: penggunaanMS.getValues(),
      keberadaanKeluarga: keberadaanKeluargaMS.getValues(),
      keberadaanBku: keberadaanBkuMS.getValues(),
      status: statusMS.getValues(),
      flagBaru: flagBaruSelect.value,
      flagRegsosek: flagRegsosekSelect.value,
      nonRespon: nonResponSelect.value,
    };
  }

  // Restoring the wilayah chain has to be sequential: each level's options
  // are fetched from the level above, so its value can't be set before
  // that fetch lands. A code that no longer exists in the reloaded list
  // simply doesn't take (the <select> falls back to ""), which is the
  // right outcome — the wilayah is gone from the data, so filtering by it
  // would return nothing anyway.
  async function applyFilterState(f) {
    const enc = encodeURIComponent;
    kabkotaSelect.value = f.kabkota || "";
    await kecamatanLevel.load(kabkotaSelect.value ? `/api/kecamatan?kabkota=${enc(kabkotaSelect.value)}` : null);
    kecamatanSelect.value = f.kecamatan || "";
    await desaLevel.load(kecamatanSelect.value
      ? `/api/desa?kabkota=${enc(kabkotaSelect.value)}&kecamatan=${enc(kecamatanSelect.value)}`
      : null);
    desaSelect.value = f.desa || "";
    await slsLevel.load(desaSelect.value
      ? `/api/sls?kabkota=${enc(kabkotaSelect.value)}&kecamatan=${enc(kecamatanSelect.value)}&desa=${enc(desaSelect.value)}`
      : null);
    slsSelect.value = f.sls || "";
    await subslsLevel.load(slsSelect.value
      ? `/api/subsls?kabkota=${enc(kabkotaSelect.value)}&kecamatan=${enc(kecamatanSelect.value)}&desa=${enc(desaSelect.value)}&sls=${enc(slsSelect.value)}`
      : null);
    subslsSelect.value = f.subsls || "";

    searchInput.value = f.search || "";
    jenisPrelistMS.setValues(f.jenisPrelist);
    penggunaanMS.setValues(f.penggunaanBangunan);
    keberadaanKeluargaMS.setValues(f.keberadaanKeluarga);
    keberadaanBkuMS.setValues(f.keberadaanBku);
    statusMS.setValues(f.status);
    flagBaruSelect.value = f.flagBaru || "";
    flagRegsosekSelect.value = f.flagRegsosek || "";
    nonResponSelect.value = f.nonRespon || "";

    // Picking a preset means "show me this", so it applies straight away
    // rather than leaving the user to press Terapkan Filter as well.
    applyFilters();
  }

  App.makePresetBar({
    rootEl: document.getElementById("preset-bar-list"),
    selectEl: document.getElementById("preset-select-list"),
    saveBtn: document.getElementById("preset-save-list"),
    deleteBtn: document.getElementById("preset-delete-list"),
    statusEl: document.getElementById("preset-status-list"),
    read: readFilterState,
    apply: applyFilterState,
  });


  // --- filter panel collapse ---------------------------------------------
  // The toggle is available at every width: on a phone the controls stack
  // to roughly a full screen, and even on a wide screen the card is around
  // 300px of the viewport that the table could be using instead. What the
  // breakpoint still decides is the *default* — collapsed on a phone,
  // open on anything wider.

  function setFiltersCollapsed(collapsed) {
    filtersEl.classList.toggle("collapsed", collapsed);
    filterToggleBtn.setAttribute("aria-expanded", String(!collapsed));
  }

  filterToggleBtn.addEventListener("click", () => {
    setFiltersCollapsed(!filtersEl.classList.contains("collapsed"));
  });

  // Default state per breakpoint: collapsed on a phone, open above it.
  // Tracked rather than read once at load, because the page can start
  // narrow and then be widened (window resize, phone rotation) and the
  // sensible default differs on each side of that line.
  const narrowQuery = window.matchMedia("(max-width: 600px)");
  narrowQuery.addEventListener("change", (e) => setFiltersCollapsed(e.matches));
  setFiltersCollapsed(narrowQuery.matches);

  // Applying on a phone means you are done choosing — fold the panel away
  // so the results are what fills the screen.
  applyFilterBtn.addEventListener("click", () => {
    if (narrowQuery.matches) setFiltersCollapsed(true);
  });

  // --- PDF / Excel download ----------------------------------------------

  // Shared by both formats — same filter/sort params, same "build a
  // temporary link and click it" approach (works for any
  // Content-Disposition: attachment endpoint, and keeps this an explicit,
  // self-contained action rather than ever touching location.href).
  // split adds split=subsls: one file per SubSLS in a ZIP instead of one
  // report — same endpoint, see report_split.go on the server. unlock is
  // the token /api/report-unlock handed out for the second password, only
  // needed (and only asked for) at kabupaten/kota width.
  function downloadReport(apiPath, split, unlock) {
    const params = buildFilterParams();
    params.set("sortBy", sortColumn);
    params.set("dir", sortDir);
    if (split) params.set("split", "subsls");
    if (unlock) params.set("unlock", unlock);
    const url = `${apiPath}?${params}`;
    const a = document.createElement("a");
    a.href = url;
    a.rel = "noopener";
    document.body.appendChild(a);
    a.click();
    a.remove();
  }

  // The download dialog: Unduh PDF/Excel open it, the actual download
  // starts from its Unduh button. Its one option, "Pisahkan per SubSLS",
  // is always the user's to flip: on gives one ZIP with a file per SubSLS,
  // off one file for the whole scope, at any width — the note under it
  // carries the row count so a whole-kecamatan or whole-kabupaten/kota
  // file is a knowing choice. It defaults on above desa width (where one
  // file is huge) and otherwise to the user's last choice, remembered for
  // the session, not persisted: a switch that silently stays on across
  // days would hand out ZIPs to someone who expected a PDF.
  //
  // Kabupaten/kota width — and only that — also shows the second-password
  // row, whichever way the switch is set: the password is exchanged for a
  // token at /api/report-unlock first, so a wrong one is reported inside
  // the dialog and the password itself never appears in a download URL.
  const SPLIT_NOTE_FREE = "Aktif: satu ZIP berisi satu file per SubSLS. Nonaktif: satu file untuk seluruh cakupan.";
  const SPLIT_NOTE_SINGLE = "Filter sudah satu SubSLS; aktif berarti ZIP berisi satu file itu saja.";
  const SPLIT_NOTE_WIDE = "Aktif: satu ZIP berisi satu file per SubSLS. Nonaktif: satu file utuh untuk seluruh cakupan (%ROWS% baris).";
  // Measured: a 112k-row kecamatan is a 47 MB PDF in ~9 s; Samarinda
  // (552k rows) a 224 MB PDF in ~48 s. Below this the warning would just
  // be noise.
  const BIG_REPORT_ROWS = 50000;
  const SPLIT_NOTE_BIG = " File utuhnya besar (puluhan sampai ratusan MB) dan prosesnya bisa sekitar satu menit.";
  const SPLIT_NOTE_KABKOTA = " Cakupan satu kabupaten/kota memerlukan password tambahan.";
  const UNLOCK_ERRORS = {
    403: "Password salah.",
    503: "Unduhan satu kabupaten/kota tidak diaktifkan di server ini.",
  };
  let pendingDownloadPath = "";
  let splitPreferred = false;
  let unlockPending = false;

  // "Kutai Timur › Kec. Sangatta Utara › Desa/Kel. Sangatta Utara", from
  // the option labels of the applied codes. Falls back to the bare code
  // when a select has since been re-populated for another parent (the
  // user changed a select without applying).
  function appliedScopeText() {
    const name = (sel, code) => {
      const opt = [...sel.options].find((o) => o.value === code);
      // The option labels carry a row count ("Kutai Timur (201.291 data)")
      // that is about the option list, not this download.
      return opt ? opt.textContent.replace(/\s*\([\d.]+ data\)\s*$/, "").trim() : code;
    };
    const parts = [];
    if (appliedKabkota) parts.push(name(kabkotaSelect, appliedKabkota));
    if (appliedKecamatan) parts.push(name(kecamatanSelect, appliedKecamatan));
    if (appliedDesa) parts.push(name(desaSelect, appliedDesa));
    if (appliedSls) parts.push(name(slsSelect, appliedSls));
    if (appliedSubsls) parts.push(name(subslsSelect, appliedSubsls));
    return parts.join(" \u203a ");
  }

  function confirmLabel(kind) {
    return splitToggle.checked ? "Unduh ZIP" : `Unduh ${kind}`;
  }

  function needsUnlock() {
    return !appliedKecamatan;
  }

  function openDownloadDialog(kind, apiPath) {
    pendingDownloadPath = apiPath;
    const kabkotaOnly = needsUnlock();
    const wide = !appliedDesa;
    const single = !!appliedSubsls;
    splitToggle.disabled = false;
    splitToggle.checked = wide ? true : single ? false : splitPreferred;
    downloadDialogTitle.textContent = `Unduh ${kind}`;
    downloadDialogScope.textContent = `Cakupan: ${appliedScopeText()}`;
    let note = wide
      ? SPLIT_NOTE_WIDE.replace("%ROWS%", appliedTotal.toLocaleString("id-ID"))
      : single ? SPLIT_NOTE_SINGLE : SPLIT_NOTE_FREE;
    if (wide && appliedTotal >= BIG_REPORT_ROWS) note += SPLIT_NOTE_BIG;
    if (kabkotaOnly) note += SPLIT_NOTE_KABKOTA;
    downloadDialogNote.textContent = note;
    downloadDialogPassword.hidden = !kabkotaOnly;
    downloadPasswordInput.value = "";
    setUnlockError("");
    setUnlockPending(false);
    downloadDialogConfirm.className = kind === "PDF" ? "download-pdf" : "download-xlsx";
    downloadDialogConfirm.dataset.kind = kind;
    downloadDialogConfirm.textContent = confirmLabel(kind);
    if (typeof downloadDialog.showModal !== "function") {
      // No <dialog> support: go straight to the download with the only
      // valid choice for this width. Kabupaten/kota width can't be served
      // this way (no place to ask for the password), so it stays a no-op.
      if (!kabkotaOnly) downloadReport(apiPath, splitToggle.checked);
      return;
    }
    downloadDialog.showModal();
    if (kabkotaOnly) downloadPasswordInput.focus();
  }

  function setUnlockError(msg) {
    downloadDialogError.textContent = msg;
    downloadDialogError.hidden = !msg;
  }

  function setUnlockPending(v) {
    unlockPending = v;
    downloadDialogConfirm.disabled = v;
    downloadPasswordInput.disabled = v;
  }

  // Trade the second password for a download token. Resolves to the token,
  // or to "" after showing the reason in the dialog.
  async function requestUnlock(password) {
    setUnlockPending(true);
    try {
      const res = await fetch("/api/report-unlock", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ password }),
      });
      if (!res.ok) {
        setUnlockError(UNLOCK_ERRORS[res.status] || `Gagal memverifikasi password (HTTP ${res.status}).`);
        return "";
      }
      const data = await res.json();
      return data.token || "";
    } catch (err) {
      setUnlockError("Tidak bisa menghubungi server. Coba lagi.");
      return "";
    } finally {
      setUnlockPending(false);
    }
  }

  async function confirmDownload() {
    if (unlockPending) return;
    let unlock = "";
    if (needsUnlock()) {
      const password = downloadPasswordInput.value;
      if (!password) {
        setUnlockError("Masukkan password unduh kabupaten/kota.");
        downloadPasswordInput.focus();
        return;
      }
      unlock = await requestUnlock(password);
      if (!unlock) {
        downloadPasswordInput.select();
        return;
      }
    }
    downloadDialog.close();
    downloadReport(pendingDownloadPath, splitToggle.checked, unlock);
  }

  splitToggle.addEventListener("change", () => {
    // Above desa width the default is on regardless, so only a choice made
    // at desa/SLS/SubSLS width is worth remembering.
    if (appliedDesa) splitPreferred = splitToggle.checked;
    downloadDialogConfirm.textContent = confirmLabel(downloadDialogConfirm.dataset.kind);
  });
  downloadDialogCancel.addEventListener("click", () => downloadDialog.close());
  downloadDialogConfirm.addEventListener("click", confirmDownload);
  downloadPasswordInput.addEventListener("keydown", (e) => {
    if (e.key === "Enter") {
      e.preventDefault();
      confirmDownload();
    }
  });
  downloadPasswordInput.addEventListener("input", () => setUnlockError(""));
  // A click on the backdrop (outside the body) closes, like Escape does.
  downloadDialog.addEventListener("click", (e) => {
    if (e.target === downloadDialog) downloadDialog.close();
  });

  downloadPdfBtn.addEventListener("click", () => {
    if (!downloadPdfBtn.disabled) openDownloadDialog("PDF", "/api/list/pdf");
  });
  downloadXlsxBtn.addEventListener("click", () => {
    if (!downloadXlsxBtn.disabled) openDownloadDialog("Excel", "/api/list/xlsx");
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
    loadDataUpdatedAt();
    loadPage();
  });
})();
