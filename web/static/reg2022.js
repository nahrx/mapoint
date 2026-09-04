(() => {
  "use strict";

  const { fetchWithRetry, makeCascadingLevel, esc } = App;

  const kabkotaSelect = document.getElementById("kabkota-select-reg");
  const kecamatanSelect = document.getElementById("kecamatan-select-reg");
  const desaSelect = document.getElementById("desa-select-reg");
  const slsSelect = document.getElementById("sls-select-reg");
  const subslsSelect = document.getElementById("subsls-select-reg");
  const matchStatusSelect = document.getElementById("matchstatus-select-reg");
  const searchInput = document.getElementById("reg2022-search");
  const tbody = document.getElementById("reg2022-tbody");
  const loadingEl = document.getElementById("reg2022-loading");
  const errorEl = document.getElementById("reg2022-error");
  const pageInfoEl = document.getElementById("reg2022-page-info");
  const prevBtn = document.getElementById("reg2022-prev");
  const nextBtn = document.getElementById("reg2022-next");
  const pageSizeSelect = document.getElementById("reg2022-pagesize");
  const sortHeaders = document.querySelectorAll("#reg2022-table th.sortable");
  const applyFilterBtn = document.getElementById("apply-filter-btn-reg");
  const filtersEl = document.getElementById("reg2022-filters");
  const filterToggleBtn = document.getElementById("reg2022-filter-toggle");
  const downloadPdfBtn = document.getElementById("download-pdf-btn-reg");
  const downloadXlsxBtn = document.getElementById("download-xlsx-btn-reg");

  const COLUMN_COUNT = 8; // keep in step with the <thead> in index.html

  // Match Status is what this table is read for, so each value gets a
  // badge colored by how strong the match is: green for an exact NIK hit,
  // blue for an exact SLS-name hit, and progressively warmer as the number
  // of mistyped NIK digits grows. Anything unrecognized falls back to grey
  // rather than breaking the cell.
  const MATCH_COLORS = {
    "MATCH_EXACT_NIK_HEAD": "#1e7145",
    "MATCH_EXACT_NIK_MEMBER": "#2a9d3f",
    "MATCH_EXACT_SLS_NAME_HEAD": "#2a6fb5",
    "MATCH_EXACT_SLS_NAME_MEMBER": "#4a90c8",
    "MATCH_NIK_1_DIGIT_TYPO": "#d9720f",
    "MATCH_NIK_2_DIGIT_TYPO": "#c25a10",
    "MATCH_NIK_3_DIGIT_TYPO": "#b03b32",
  };

  let kabkotaByCode = new Map();
  let kecamatanByCode = new Map();
  let desaByCode = new Map();
  let slsByCode = new Map();
  let subslsByCode = new Map();

  // Same "pending vs applied" split as the Daftar menu: changing a select
  // or typing never reloads the table on its own, it only takes effect
  // when Terapkan Filter is clicked (see applyFilters).
  let appliedKabkota = "";
  let appliedKecamatan = "";
  let appliedDesa = "";
  let appliedSls = "";
  let appliedSubsls = "";
  let appliedMatchStatus = "";
  let appliedSearch = "";

  // Must match points.EmptyValue on the server.
  const EMPTY_VALUE = "__EMPTY__";

  let currentPage = 1;
  let pageSize = 50;
  let sortColumn = "nama";
  let sortDir = "asc";
  let requestSeq = 0;

  function rowHTML(r, rowNo) {
    return `
      <tr>
        <td class="num">${rowNo}</td>
        <td>${esc(r.nama)}</td>
        <td>${esc(r.nama_kk)}</td>
        <td class="mono">${esc(r.subsls)}</td>
        <td class="col-match">${r.match_status
          ? `<span class="match-badge" style="background:${MATCH_COLORS[r.match_status] || "#8794a1"}">${esc(r.match_status)}</span>`
          : "-"}</td>
        <td class="addr" title="${esc(r.alamat_regsosek)}">${esc(r.alamat_regsosek)}</td>
        <td>${esc(r.nama_matched)}</td>
        <td class="mono">${esc(r.assignment_id)}</td>
      </tr>`;
  }

  function setLoading(v) {
    loadingEl.classList.toggle("hidden", !v);
  }

  // Filter half only — shared by the table fetch and the download links,
  // so a download always matches what is on screen.
  function buildFilterParams() {
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

  function buildParams() {
    const params = buildFilterParams();
    params.set("page", String(currentPage));
    params.set("pageSize", String(pageSize));
    params.set("sortBy", sortColumn);
    params.set("dir", sortDir);
    return params;
  }

  async function loadPage() {
    const seq = ++requestSeq;
    setLoading(true);
    errorEl.classList.add("hidden");
    try {
      const resp = await fetchWithRetry(`/api/reg2022?${buildParams()}`, undefined);
      if (seq !== requestSeq) return; // superseded by a newer request
      render(resp);
    } catch (err) {
      if (seq !== requestSeq) return;
      console.error("failed to load reg2022", err);
      errorEl.textContent = "Gagal memuat data. Coba muat ulang halaman.";
      errorEl.classList.remove("hidden");
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

  // --- wilayah filter ----------------------------------------------------
  // These reuse the Peta/Daftar cascade endpoints rather than having their
  // own: se2026_match_regsosek uses the same level_6_full_code, and every
  // wilayah in it also exists in the points table, so the option lists are
  // the same. (The "(N titik)" counts in the labels therefore come from the
  // points table, not from this one.)

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
      console.error("failed to load reg2022 filter options", err);
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

  // Both downloads need the filter pinned at least to a kecamatan (see
  // prepareRegsosekReport on the server): this table is much smaller per
  // wilayah than the points one, so a kecamatan is a sensible report while
  // requiring a desa would be needlessly strict.
  function updateDownloadButtonState() {
    const ready = !!(appliedKabkota && appliedKecamatan);
    downloadPdfBtn.disabled = !ready;
    downloadXlsxBtn.disabled = !ready;
  }

  function downloadReport(btn, apiPath) {
    if (btn.disabled) return;
    const params = buildFilterParams();
    params.set("sortBy", sortColumn);
    params.set("dir", sortDir);
    const a = document.createElement("a");
    a.href = `${apiPath}?${params}`;
    a.rel = "noopener";
    document.body.appendChild(a);
    a.click();
    a.remove();
  }

  downloadPdfBtn.addEventListener("click", () => downloadReport(downloadPdfBtn, "/api/reg2022/pdf"));
  downloadXlsxBtn.addEventListener("click", () => downloadReport(downloadXlsxBtn, "/api/reg2022/xlsx"));

  function applyFilters() {
    appliedKabkota = kabkotaSelect.value;
    appliedKecamatan = kecamatanSelect.value;
    appliedDesa = desaSelect.value;
    appliedSls = slsSelect.value;
    appliedSubsls = subslsSelect.value;
    appliedMatchStatus = matchStatusSelect.value;
    appliedSearch = searchInput.value.trim();

    updateDownloadButtonState();
    currentPage = 1;
    loadPage();
  }

  applyFilterBtn.addEventListener("click", applyFilters);

  // --- filter panel collapse (phone only) --------------------------------

  function setFiltersCollapsed(collapsed) {
    filtersEl.classList.toggle("collapsed", collapsed);
    filterToggleBtn.setAttribute("aria-expanded", String(!collapsed));
  }

  filterToggleBtn.addEventListener("click", () => {
    setFiltersCollapsed(!filtersEl.classList.contains("collapsed"));
  });

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

  // --- boot --------------------------------------------------------------

  // Like the Daftar view, this one starts hidden — defer its first query
  // until nav.js actually shows it.
  let booted = false;
  document.addEventListener("view:reg2022-shown", () => {
    if (booted) return;
    booted = true;
    updateSortHeaderUI();
    loadKabKotaOptions();
    loadFilterOptions();
    loadPage();
  });
})();
