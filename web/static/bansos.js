// Bansos menu: the rows of the `bansos` table — social-assistance
// recipients matched against the SE2026 prelist — with the same cascading
// wilayah filters as the other list menus. Data comes from GET /api/bansos.
//
// The wilayah filter is not quite the one the other menus use, and the
// difference is in the data, not here: a row is placed by its `subsls`
// when it has one and by `dtsen_kode_desa` when it doesn't, and the second
// only reaches desa level. The server does all of that (see
// bansos.Filter.clause); this file just sends the five codes.
(() => {
  "use strict";

  const { fetchWithRetry, makeCascadingLevel, esc } = App;

  const kabkotaSelect = document.getElementById("kabkota-select-bansos");
  const kecamatanSelect = document.getElementById("kecamatan-select-bansos");
  const desaSelect = document.getElementById("desa-select-bansos");
  const slsSelect = document.getElementById("sls-select-bansos");
  const subslsSelect = document.getElementById("subsls-select-bansos");
  const searchInput = document.getElementById("bansos-search");
  const tbody = document.getElementById("bansos-tbody");
  const loadingEl = document.getElementById("bansos-loading");
  const errorEl = document.getElementById("bansos-error");
  const pageInfoEl = document.getElementById("bansos-page-info");
  const prevBtn = document.getElementById("bansos-prev");
  const nextBtn = document.getElementById("bansos-next");
  const pageSizeSelect = document.getElementById("bansos-pagesize");
  const applyFilterBtn = document.getElementById("apply-filter-btn-bansos");
  const filtersEl = document.getElementById("bansos-filters");
  const filterToggleBtn = document.getElementById("bansos-filter-toggle");
  const downloadPdfBtn = document.getElementById("download-pdf-btn-bansos");
  const downloadXlsxBtn = document.getElementById("download-xlsx-btn-bansos");
  const sortHeaders = document.querySelectorAll("#bansos-table th.sortable");
  const downloadDialog = document.getElementById("bansos-download-dialog");
  const downloadTitle = document.getElementById("bansos-download-title");
  const downloadScope = document.getElementById("bansos-download-scope");
  const downloadNote = document.getElementById("bansos-download-note");
  const downloadCancel = document.getElementById("bansos-download-cancel");
  const downloadConfirm = document.getElementById("bansos-download-confirm");
  const splitRadios = [...document.querySelectorAll('#bansos-split-choice input[name="bansos-split-level"]')];

  const COLUMN_COUNT = 9; // keep in step with the <thead> in index.html

  let kabkotaByCode = new Map();
  let kecamatanByCode = new Map();
  let desaByCode = new Map();
  let slsByCode = new Map();
  let subslsByCode = new Map();

  // Same pending/applied split as every other menu: changing a select
  // cascades its own child dropdown but never reloads the table. Only
  // Terapkan Filter does that.
  let appliedKabkota = "";
  let appliedKecamatan = "";
  let appliedDesa = "";
  let appliedSls = "";
  let appliedSubsls = "";
  let appliedSearch = "";

  let currentPage = 1;
  let pageSize = 50;
  let requestSeq = 0;
  let sortColumn = "bansos_nama";
  let sortDir = "asc";

  function rowHTML(r, rowNo) {
    return `
      <tr>
        <td class="num">${rowNo}</td>
        <td>${esc(r.bansos_nama)}</td>
        <td>${esc(r.subsls)}</td>
        <td class="mono">${esc(r.assignment_id)}</td>
        <td>${esc(r.dtsen_kode_desa)}</td>
        <td class="addr" title="${esc(r.dtsen_alamat)}">${esc(r.dtsen_alamat)}</td>
        <td class="num">${esc(r.rt_ktp)}</td>
        <td class="num">${esc(r.rw_ktp)}</td>
        <td class="addr" title="${esc(r.alamat_ktp)}">${esc(r.alamat_ktp)}</td>
      </tr>`;
  }

  function setLoading(v) {
    loadingEl.classList.toggle("hidden", !v);
  }

  // The wilayah part only — shared by the table fetch and the two
  // downloads, so a download always covers exactly what is on screen.
  function buildFilterParams() {
    const params = new URLSearchParams();
    if (appliedKabkota) params.set("kabkota", appliedKabkota);
    if (appliedKecamatan) params.set("kecamatan", appliedKecamatan);
    if (appliedDesa) params.set("desa", appliedDesa);
    if (appliedSls) params.set("sls", appliedSls);
    if (appliedSubsls) params.set("subsls", appliedSubsls);
    if (appliedSearch) params.set("search", appliedSearch);
    // The sort belongs here, not in buildParams: a download should come
    // out in the order shown on screen.
    params.set("sortBy", sortColumn);
    params.set("dir", sortDir);
    return params;
  }

  function buildParams() {
    const params = buildFilterParams();
    params.set("page", String(currentPage));
    params.set("pageSize", String(pageSize));
    return params;
  }

  async function loadPage() {
    const seq = ++requestSeq;
    setLoading(true);
    errorEl.classList.add("hidden");
    try {
      const resp = await fetchWithRetry(`/api/bansos?${buildParams()}`, undefined);
      // A slower earlier request must never overwrite a newer answer.
      if (seq !== requestSeq) return;
      render(resp);
    } catch (err) {
      if (seq !== requestSeq) return;
      console.error("failed to load bansos page", err);
      errorEl.textContent = "Gagal memuat data. Coba lagi.";
      errorEl.classList.remove("hidden");
      tbody.innerHTML = "";
      pageInfoEl.textContent = "—";
    } finally {
      if (seq === requestSeq) setLoading(false);
    }
  }

  function render(resp) {
    const startRow = (resp.page - 1) * resp.page_size + 1;
    tbody.innerHTML = resp.items.length
      ? resp.items.map((r, i) => rowHTML(r, startRow + i)).join("")
      : `<tr><td colspan="${COLUMN_COUNT}" class="empty-row">Tidak ada data yang cocok dengan filter ini.</td></tr>`;

    const totalPages = Math.max(1, Math.ceil(resp.total / resp.page_size));
    pageInfoEl.textContent = `Halaman ${resp.page.toLocaleString("id-ID")} dari ${totalPages.toLocaleString("id-ID")} (${resp.total.toLocaleString("id-ID")} data)`;
    prevBtn.disabled = resp.page <= 1;
    nextBtn.disabled = resp.page >= totalPages;
    updateSortHeaderUI();
  }

  function updateSortHeaderUI() {
    for (const th of sortHeaders) {
      const active = th.dataset.sort === sortColumn;
      th.classList.toggle("sort-active", active);
      th.dataset.dir = active ? sortDir : "";
    }
  }

  // Click a header to sort by it; click the same one again to flip the
  // direction. Sorting is a server-side ORDER BY over the whole filtered
  // set, not a reshuffle of the page on screen, so page 1 is where the
  // new order starts.
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

  searchInput.addEventListener("keydown", (e) => {
    if (e.key === "Enter") applyFilters();
  });

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

  // --- wilayah filters ----------------------------------------------------

  const kecamatanLevel = makeCascadingLevel({
    selectEl: kecamatanSelect, placeholder: "Semua Kecamatan", byCode: kecamatanByCode, labelPrefix: "Kec. ",
  });
  const desaLevel = makeCascadingLevel({
    selectEl: desaSelect, placeholder: "Semua Desa/Kelurahan", byCode: desaByCode, labelPrefix: "Desa/Kel. ",
  });
  const slsLevel = makeCascadingLevel({
    selectEl: slsSelect, placeholder: "Semua SLS", byCode: slsByCode, labelPrefix: "SLS ", keepCode: true,
  });
  const subslsLevel = makeCascadingLevel({
    selectEl: subslsSelect, placeholder: "Semua SubSLS", byCode: subslsByCode, labelPrefix: "SubSLS ",
  });

  // The dropdowns are the SE2026 wilayah lists, the same ones the Daftar
  // and Reg2022 menus use, counts and all — those counts describe
  // se2026_titik2, not this table. They are stripped in one place only:
  // the download dialog's scope line, where a point count sitting next to
  // "Unduh" would read as the size of the file about to be produced.
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

  const enc = encodeURIComponent;

  kabkotaSelect.addEventListener("change", async () => {
    const kabkota = kabkotaSelect.value;
    desaLevel.reset();
    desaSelect.disabled = true;
    slsLevel.reset();
    slsSelect.disabled = true;
    subslsLevel.reset();
    subslsSelect.disabled = true;
    await kecamatanLevel.load(kabkota ? `/api/kecamatan?kabkota=${enc(kabkota)}` : null);
  });

  kecamatanSelect.addEventListener("change", async () => {
    const kabkota = kabkotaSelect.value;
    const kecamatan = kecamatanSelect.value;
    slsLevel.reset();
    slsSelect.disabled = true;
    subslsLevel.reset();
    subslsSelect.disabled = true;
    await desaLevel.load(
      kecamatan ? `/api/desa?kabkota=${enc(kabkota)}&kecamatan=${enc(kecamatan)}` : null
    );
  });

  desaSelect.addEventListener("change", async () => {
    const kabkota = kabkotaSelect.value;
    const kecamatan = kecamatanSelect.value;
    const desa = desaSelect.value;
    subslsLevel.reset();
    subslsSelect.disabled = true;
    await slsLevel.load(
      desa ? `/api/sls?kabkota=${enc(kabkota)}&kecamatan=${enc(kecamatan)}&desa=${enc(desa)}` : null
    );
  });

  slsSelect.addEventListener("change", async () => {
    const kabkota = kabkotaSelect.value;
    const kecamatan = kecamatanSelect.value;
    const desa = desaSelect.value;
    const sls = slsSelect.value;
    await subslsLevel.load(
      sls
        ? `/api/subsls?kabkota=${enc(kabkota)}&kecamatan=${enc(kecamatan)}&desa=${enc(desa)}&sls=${enc(sls)}`
        : null
    );
  });

  function applyFilters() {
    appliedKabkota = kabkotaSelect.value;
    appliedKecamatan = kecamatanSelect.value;
    appliedDesa = desaSelect.value;
    appliedSls = slsSelect.value;
    appliedSubsls = subslsSelect.value;
    appliedSearch = searchInput.value.trim();
    currentPage = 1;
    loadPage();
  }
  applyFilterBtn.addEventListener("click", applyFilters);

  // --- PDF / Excel download ----------------------------------------------

  // Unduh PDF/Excel open the options dialog; the download itself starts
  // from its Unduh button. Same "build a temporary link and click it"
  // approach as the other menus, and the download covers the *applied*
  // filter and the current sort, so it matches the rows on screen.
  function downloadReport(apiPath, split) {
    const params = buildFilterParams();
    if (split) params.set("split", split);
    const a = document.createElement("a");
    a.href = `${apiPath}?${params}`;
    a.rel = "noopener";
    document.body.appendChild(a);
    a.click();
    a.remove();
  }

  // Unlike the Daftar menu there is no minimum wilayah and no second
  // password here (16k rows in total), so every shape is available at
  // every width — including no filter at all. What does vary is how many
  // rows a level can place: see SPLIT_NOTE.
  const SPLIT_NOTE = {
    "": "Satu file berisi seluruh cakupan.",
    desa: "Satu ZIP berisi satu file per desa/kelurahan. Baris tanpa ID SubSLS memakai Kode Desa DTSEN-nya.",
    sls: "Satu ZIP berisi satu file per SLS. Hanya baris yang punya ID SubSLS yang bisa ditempatkan.",
    subsls: "Satu ZIP berisi satu file per SubSLS. Hanya baris yang punya ID SubSLS yang bisa ditempatkan.",
  };
  const SPLIT_NOTE_UNPLACED = " Baris yang tidak bisa ditempatkan tetap ikut, dikumpulkan di satu file terpisah di dalam ZIP.";
  let pendingPath = "";
  let splitPreferred = "";

  function splitLevel() {
    const r = splitRadios.find((x) => x.checked);
    return r ? r.value : "";
  }

  function appliedScopeText() {
    const name = (sel, code) => {
      const opt = [...sel.options].find((o) => o.value === code);
      if (!opt) return code;
      // Drop the "(12.345 titik)" the shared wilayah lists carry.
      return opt.textContent.replace(/\s*\([\d.]+ (titik|data)\)\s*$/, "").trim();
    };
    const parts = [];
    if (appliedKabkota) parts.push(name(kabkotaSelect, appliedKabkota));
    if (appliedKecamatan) parts.push(name(kecamatanSelect, appliedKecamatan));
    if (appliedDesa) parts.push(name(desaSelect, appliedDesa));
    if (appliedSls) parts.push(name(slsSelect, appliedSls));
    if (appliedSubsls) parts.push(name(subslsSelect, appliedSubsls));
    if (!parts.length) parts.push("seluruh data");
    let text = "Cakupan: " + parts.join(" \u203a ");
    if (appliedSearch) text += ` — cari nama "${appliedSearch}"`;
    return text;
  }

  function updateDialogNote() {
    const level = splitLevel();
    let note = SPLIT_NOTE[level];
    if (level) note += SPLIT_NOTE_UNPLACED;
    downloadNote.textContent = note;
    downloadConfirm.textContent = level ? "Unduh ZIP" : `Unduh ${downloadConfirm.dataset.kind}`;
  }

  function openDownloadDialog(kind, apiPath) {
    pendingPath = apiPath;
    for (const r of splitRadios) r.checked = r.value === splitPreferred;
    downloadTitle.textContent = `Unduh ${kind}`;
    downloadScope.textContent = appliedScopeText();
    downloadConfirm.className = kind === "PDF" ? "download-pdf" : "download-xlsx";
    downloadConfirm.dataset.kind = kind;
    updateDialogNote();
    if (typeof downloadDialog.showModal !== "function") {
      downloadReport(apiPath, splitLevel());
      return;
    }
    downloadDialog.showModal();
  }

  for (const r of splitRadios) {
    r.addEventListener("change", () => {
      splitPreferred = splitLevel();
      updateDialogNote();
    });
  }
  downloadCancel.addEventListener("click", () => downloadDialog.close());
  downloadConfirm.addEventListener("click", () => {
    downloadDialog.close();
    downloadReport(pendingPath, splitLevel());
  });
  // A click on the backdrop closes, like Escape does.
  downloadDialog.addEventListener("click", (e) => {
    if (e.target === downloadDialog) downloadDialog.close();
  });

  downloadPdfBtn.addEventListener("click", () => openDownloadDialog("PDF", "/api/bansos/pdf"));
  downloadXlsxBtn.addEventListener("click", () => openDownloadDialog("Excel", "/api/bansos/xlsx"));

  // --- filter panel collapse (same rules as the other list menus) ---------

  function setFiltersCollapsed(collapsed) {
    filtersEl.classList.toggle("collapsed", collapsed);
    filterToggleBtn.setAttribute("aria-expanded", String(!collapsed));
  }
  filterToggleBtn.addEventListener("click", () => {
    setFiltersCollapsed(!filtersEl.classList.contains("collapsed"));
  });
  const narrowQuery = window.matchMedia("(max-width: 600px)");
  narrowQuery.addEventListener("change", (e) => setFiltersCollapsed(e.matches));
  setFiltersCollapsed(narrowQuery.matches);
  applyFilterBtn.addEventListener("click", () => {
    if (narrowQuery.matches) setFiltersCollapsed(true);
  });

  // --- boot ---------------------------------------------------------------

  // Hidden until nav.js shows it, so its first query doesn't race the
  // Peta view's.
  let booted = false;
  document.addEventListener("view:bansos-shown", () => {
    if (booted) return;
    booted = true;
    updateSortHeaderUI();
    loadKabKotaOptions();
    loadPage();
  });
})();
