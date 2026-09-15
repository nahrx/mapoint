// Tabulasi menu: one tab per categorical variable, each a cross-tabulation
// of that variable against SubSLS — rows are level_6_full_code, columns are
// the variable's categories, cells are row counts. Data comes from
// GET /api/tabulasi; the tab list itself from GET /api/tabulasi/variables,
// so the menu can never offer a variable the server doesn't validate.
(() => {
  "use strict";

  const { fetchWithRetry, makeCascadingLevel, esc } = App;

  const tabsEl = document.getElementById("tabulasi-tabs");
  const kabkotaSelect = document.getElementById("kabkota-select-tab");
  const kecamatanSelect = document.getElementById("kecamatan-select-tab");
  const desaSelect = document.getElementById("desa-select-tab");
  const slsSelect = document.getElementById("sls-select-tab");
  const subslsSelect = document.getElementById("subsls-select-tab");
  const applyFilterBtn = document.getElementById("apply-filter-btn-tab");
  const filtersEl = document.getElementById("tabulasi-filters");
  const filterToggleBtn = document.getElementById("tabulasi-filter-toggle");
  const theadEl = document.getElementById("tabulasi-thead");
  const tbodyEl = document.getElementById("tabulasi-tbody");
  const tfootEl = document.getElementById("tabulasi-tfoot");
  const loadingEl = document.getElementById("tabulasi-loading");
  const errorEl = document.getElementById("tabulasi-error");
  const pageInfoEl = document.getElementById("tabulasi-page-info");
  const prevBtn = document.getElementById("tabulasi-prev");
  const nextBtn = document.getElementById("tabulasi-next");
  const pageSizeSelect = document.getElementById("tabulasi-pagesize");
  const downloadXlsxBtn = document.getElementById("download-xlsx-btn-tab");

  let variables = [];      // from /api/tabulasi/variables, in tab order
  let currentVar = "";     // key of the active tab

  let kabkotaByCode = new Map();
  let kecamatanByCode = new Map();
  let desaByCode = new Map();
  let slsByCode = new Map();
  let subslsByCode = new Map();

  // Same pending/applied split as every other menu: the selects cascade
  // their own child options live, but the table only reloads on Terapkan
  // Filter (or a tab change, which keeps the applied wilayah).
  let appliedKabkota = "";
  let appliedKecamatan = "";
  let appliedDesa = "";
  let appliedSls = "";
  let appliedSubsls = "";

  let currentPage = 1;
  let pageSize = parseInt(pageSizeSelect.value, 10) || 50;
  let requestSeq = 0;

  // Sort order, sent to the server as sortBy / category / dir. Every column
  // is sortable; "category" names one of the variable's categories via
  // sortCategory (the blank column travels as App.EMPTY_VALUE, the same
  // sentinel the filters use).
  let sortKey = "subsls";
  let sortCategory = "";
  let sortDir = "asc";

  const fmt = (n) => n.toLocaleString("id-ID");

  // --- tabs ---------------------------------------------------------------

  function renderTabs() {
    tabsEl.innerHTML = "";
    for (const v of variables) {
      const btn = document.createElement("button");
      btn.type = "button";
      btn.className = "tab-btn";
      btn.textContent = v.label;
      btn.dataset.key = v.key;
      btn.setAttribute("role", "tab");
      btn.setAttribute("aria-selected", String(v.key === currentVar));
      btn.addEventListener("click", () => selectVar(v.key));
      tabsEl.appendChild(btn);
    }
  }

  function selectVar(key) {
    if (key === currentVar) return;
    currentVar = key;
    for (const btn of tabsEl.querySelectorAll(".tab-btn")) {
      btn.setAttribute("aria-selected", String(btn.dataset.key === key));
    }
    // A new variable is a new table shape; page 1 is the only page that
    // means the same thing on both — and a sort by one of the old
    // variable's categories means nothing on the new one.
    if (sortKey === "category") {
      sortKey = "subsls";
      sortCategory = "";
      sortDir = "asc";
    }
    currentPage = 1;
    loadPage();
  }

  async function loadVariables() {
    variables = await fetchWithRetry("/api/tabulasi/variables", undefined);
    if (!currentVar && variables.length) currentVar = variables[0].key;
    renderTabs();
  }

  // --- data -------------------------------------------------------------

  function buildParams() {
    const params = new URLSearchParams({ var: currentVar });
    if (appliedKabkota) params.set("kabkota", appliedKabkota);
    if (appliedKecamatan) params.set("kecamatan", appliedKecamatan);
    if (appliedDesa) params.set("desa", appliedDesa);
    if (appliedSls) params.set("sls", appliedSls);
    if (appliedSubsls) params.set("subsls", appliedSubsls);
    params.set("page", String(currentPage));
    params.set("pageSize", String(pageSize));
    params.set("sortBy", sortKey);
    if (sortKey === "category") params.set("category", sortCategory === "" ? App.EMPTY_VALUE : sortCategory);
    params.set("dir", sortDir);
    return params;
  }

  function setLoading(v) {
    loadingEl.classList.toggle("hidden", !v);
  }

  async function loadPage() {
    if (!currentVar) return;
    const seq = ++requestSeq;
    setLoading(true);
    errorEl.classList.add("hidden");
    try {
      const resp = await fetchWithRetry(`/api/tabulasi?${buildParams()}`, undefined);
      if (seq !== requestSeq) return; // superseded
      render(resp);
    } catch (err) {
      if (seq !== requestSeq) return;
      console.error("failed to load tabulasi", err);
      errorEl.textContent = "Gagal memuat tabulasi. Coba muat ulang halaman.";
      errorEl.classList.remove("hidden");
    } finally {
      if (seq === requestSeq) setLoading(false);
    }
  }

  // A zero is drawn as a muted dash: a tabulation is mostly zeros, and a
  // grid of "0"s hides the numbers that matter. The cell still carries the
  // real value in its title for anyone who wants it spelled out.
  function cell(n, cls) {
    if (!n) return `<td class="num tab-zero${cls ? " " + cls : ""}" title="0">–</td>`;
    return `<td class="num${cls ? " " + cls : ""}">${fmt(n)}</td>`;
  }

  function colLabel(c) {
    return c === "" ? "(Kosong)" : c;
  }

  function render(resp) {
    const cols = resp.columns;

    // Every header but "No" sorts. Category headers carry the raw category
    // value in data-cat (blank for the "(Kosong)" column) so the click
    // handler doesn't have to map a label back to a value.
    const th = (key, label, cls, extra = "") =>
      `<th class="sortable${cls ? " " + cls : ""}" data-sort="${key}"${extra} title="${esc(label)}">${esc(label)}<span class="sort-arrow"></span></th>`;
    theadEl.innerHTML = `<tr>
      <th class="num">No</th>
      ${th("kabkota", "Kabupaten/Kota")}
      ${th("kecamatan", "Kecamatan")}
      ${th("desa", "Desa/Kelurahan")}
      ${th("sls", "Nama SLS")}
      ${th("subsls", "ID SUBSLS", "tab-subsls")}
      ${cols.map((c) => th("category", colLabel(c), "num tab-cat" + (c === "" ? " tab-cat-empty" : ""), ` data-cat="${esc(c)}"`)).join("")}
      ${th("total", "Total", "num tab-total")}
    </tr>`;
    for (const cell of theadEl.querySelectorAll("th.sortable")) {
      cell.addEventListener("click", () => onSortClick(cell));
    }
    updateSortHeaderUI();

    const startRow = (resp.page - 1) * resp.page_size + 1;
    tbodyEl.innerHTML = resp.rows.length
      ? resp.rows.map((r, i) => `<tr>
          <td class="num">${startRow + i}</td>
          <td>${esc(r.kabkota_name || "-")}</td>
          <td>${esc(r.kecamatan_name || "-")}</td>
          <td>${esc(r.desa_name || "-")}</td>
          <td class="tab-sls-name">${esc(r.sls_name || "-")}</td>
          <td class="tab-subsls mono">${esc(r.subsls)}</td>
          ${cols.map((c) => cell(r.counts[c] || 0)).join("")}
          ${cell(r.total, "tab-total")}
        </tr>`).join("")
      : `<tr><td colspan="${cols.length + 7}" class="empty-row">Tidak ada data yang cocok dengan filter ini.</td></tr>`;

    // The footer sums the whole filter, not the page: paging through a
    // kabupaten shouldn't make the total row jump around.
    tfootEl.innerHTML = resp.rows.length
      ? `<tr>
          <th colspan="6" class="tab-grand-label">Total (${fmt(resp.total_rows)} SubSLS)</th>
          ${cols.map((c) => cell(resp.grand[c] || 0, "tab-grand")).join("")}
          ${cell(resp.grand_total, "tab-grand tab-total")}
        </tr>`
      : "";

    const totalPages = Math.max(1, Math.ceil(resp.total_rows / resp.page_size));
    pageInfoEl.textContent = `Halaman ${fmt(resp.page)} dari ${fmt(totalPages)} (${fmt(resp.total_rows)} SubSLS, ${fmt(resp.grand_total)} data)`;
    prevBtn.disabled = resp.page <= 1;
    nextBtn.disabled = resp.page >= totalPages;
  }

  // Clicking a header sorts by it; clicking the active one flips the
  // direction. A first click on a count column starts descending — when
  // someone sorts a tabulation by a category they are looking for where
  // it is largest, not for the zeros — while text columns start ascending.
  function onSortClick(cell) {
    const key = cell.dataset.sort;
    const cat = key === "category" ? (cell.dataset.cat || "") : "";
    const same = key === sortKey && (key !== "category" || cat === sortCategory);
    if (same) {
      sortDir = sortDir === "asc" ? "desc" : "asc";
    } else {
      sortKey = key;
      sortCategory = cat;
      sortDir = (key === "category" || key === "total") ? "desc" : "asc";
    }
    currentPage = 1;
    loadPage();
  }

  function updateSortHeaderUI() {
    for (const cell of theadEl.querySelectorAll("th.sortable")) {
      const key = cell.dataset.sort;
      const active = key === sortKey && (key !== "category" || (cell.dataset.cat || "") === sortCategory);
      cell.classList.toggle("sort-active", active);
      cell.dataset.dir = active ? sortDir : "";
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

  // --- wilayah filter -----------------------------------------------------

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

  async function loadKabKotaOptions() {
    try {
      const list = await fetchWithRetry("/api/kabkota", undefined);
      for (const k of list) {
        kabkotaByCode.set(k.code, k);
        const opt = document.createElement("option");
        opt.value = k.code;
        // "data", not "titik": every row counts here, coordinates or not —
        // same reasoning as the Daftar menu.
        opt.textContent = `${k.name} (${fmt(k.total)} data)`;
        kabkotaSelect.appendChild(opt);
      }
    } catch (err) {
      console.error("failed to load kabkota list", err);
    }
  }

  const enc = encodeURIComponent;

  kabkotaSelect.addEventListener("change", async () => {
    desaLevel.reset(); desaSelect.disabled = true;
    slsLevel.reset(); slsSelect.disabled = true;
    subslsLevel.reset(); subslsSelect.disabled = true;
    const kabkota = kabkotaSelect.value;
    await kecamatanLevel.load(kabkota ? `/api/kecamatan?kabkota=${enc(kabkota)}` : null);
  });
  kecamatanSelect.addEventListener("change", async () => {
    slsLevel.reset(); slsSelect.disabled = true;
    subslsLevel.reset(); subslsSelect.disabled = true;
    const kabkota = kabkotaSelect.value, kecamatan = kecamatanSelect.value;
    await desaLevel.load(kecamatan ? `/api/desa?kabkota=${enc(kabkota)}&kecamatan=${enc(kecamatan)}` : null);
  });
  desaSelect.addEventListener("change", async () => {
    subslsLevel.reset(); subslsSelect.disabled = true;
    const kabkota = kabkotaSelect.value, kecamatan = kecamatanSelect.value, desa = desaSelect.value;
    await slsLevel.load(desa ? `/api/sls?kabkota=${enc(kabkota)}&kecamatan=${enc(kecamatan)}&desa=${enc(desa)}` : null);
  });
  slsSelect.addEventListener("change", async () => {
    const kabkota = kabkotaSelect.value, kecamatan = kecamatanSelect.value, desa = desaSelect.value, sls = slsSelect.value;
    await subslsLevel.load(sls
      ? `/api/subsls?kabkota=${enc(kabkota)}&kecamatan=${enc(kecamatan)}&desa=${enc(desa)}&sls=${enc(sls)}`
      : null);
  });

  // The workbook holds all five variables for the *applied* wilayah — the
  // same scope the table on screen shows, not whatever the dropdowns
  // currently hold but haven't been applied. Every SubSLS, not one page.
  // Always enabled: unlike the Daftar report there is no minimum wilayah,
  // because the whole province is 17k aggregated rows, not 2 million.
  function downloadXlsx() {
    const params = new URLSearchParams();
    if (appliedKabkota) params.set("kabkota", appliedKabkota);
    if (appliedKecamatan) params.set("kecamatan", appliedKecamatan);
    if (appliedDesa) params.set("desa", appliedDesa);
    if (appliedSls) params.set("sls", appliedSls);
    if (appliedSubsls) params.set("subsls", appliedSubsls);
    const a = document.createElement("a");
    a.href = `/api/tabulasi/xlsx?${params}`;
    a.rel = "noopener";
    document.body.appendChild(a);
    a.click();
    a.remove();
  }
  downloadXlsxBtn.addEventListener("click", downloadXlsx);

  function applyFilters() {
    appliedKabkota = kabkotaSelect.value;
    appliedKecamatan = kecamatanSelect.value;
    appliedDesa = desaSelect.value;
    appliedSls = slsSelect.value;
    appliedSubsls = subslsSelect.value;
    currentPage = 1;
    loadPage();
  }
  applyFilterBtn.addEventListener("click", applyFilters);

  // --- filter panel collapse (same rules as the Daftar menu) ----------------

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

  // Hidden until nav.js shows it, so its first queries don't race the
  // Peta view's.
  let booted = false;
  document.addEventListener("view:tabulasi-shown", async () => {
    if (booted) return;
    booted = true;
    loadKabKotaOptions();
    try {
      await loadVariables();
    } catch (err) {
      console.error("failed to load tabulasi variables", err);
      errorEl.textContent = "Gagal memuat daftar variabel. Coba muat ulang halaman.";
      errorEl.classList.remove("hidden");
      return;
    }
    loadPage();
  });
})();
