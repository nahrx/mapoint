// Shared helpers used by both app.js (Peta) and daftar.js (Daftar), loaded
// before either of them. Exposed as window.App to keep a single global
// instead of scattering ad-hoc names.
window.App = (() => {
  "use strict";

  // Fetches url, retrying transient failures (network hiccups) up to twice
  // with a short backoff. Never retries an aborted request.
  async function fetchWithRetry(url, signal, attempt = 0) {
    try {
      const res = await fetch(url, { signal });
      // A session that expired mid-session isn't a transient failure —
      // retrying just delays the inevitable and ends in a generic error
      // message. Send the user to the login form instead, remembering
      // where they were so they come back to it.
      if (res.status === 401) {
        location.replace("/login?next=" + encodeURIComponent(location.pathname + location.search));
        // Never settles: the navigation is already underway, and rejecting
        // here would flash an error banner on a page that's leaving.
        return new Promise(() => {});
      }
      if (!res.ok) {
        const body = await res.json().catch(() => ({}));
        throw new Error(body.error || `HTTP ${res.status}`);
      }
      return await res.json();
    } catch (err) {
      if (err.name === "AbortError") throw err;
      if (attempt >= 2) throw err;
      await new Promise((r) => setTimeout(r, 400 * (attempt + 1)));
      return fetchWithRetry(url, signal, attempt + 1);
    }
  }

  // Generic wilayah cascading-dropdown level: fetches and renders options
  // for a <select> whose contents depend on the level(s) above it (e.g.
  // kecamatan depends on kabkota). Populates byCode (a Map the caller
  // owns) as a side effect so callers can look up bounding boxes / names
  // for the selected option.
  // keepCode changes how an item's PostGIS name is shown. For kecamatan and
  // desa the name replaces the code — nobody refers to "Kec. 060", they say
  // "Kec. Bontang Selatan". For SLS the code is what people actually work
  // from (it's part of level_6_full_code), so the name is added alongside
  // it instead of replacing it.
  // countNoun is the word after the row count in an option's label. It
  // defaults to "titik" for the map menus, where every listed wilayah is
  // about points to plot. The Daftar menu passes "data": that count is
  // every row in the wilayah, including rows with no usable coordinate,
  // so calling them "titik" would overstate what's actually mappable.
  function makeCascadingLevel({ selectEl, placeholder, byCode, labelPrefix, keepCode = false, countNoun = "titik" }) {
    function reset() {
      byCode.clear();
      selectEl.innerHTML = `<option value="">${placeholder}</option>`;
    }
    async function load(apiUrl) {
      reset();
      if (!apiUrl) {
        selectEl.disabled = true;
        return;
      }
      selectEl.disabled = false;
      try {
        const list = await fetchWithRetry(apiUrl, undefined);
        for (const item of list) {
          byCode.set(item.code, item);
          const opt = document.createElement("option");
          opt.value = item.code;
          // item.name comes from an optional PostGIS lookup on the server
          // (kecamatan, desa and SLS). When it isn't set — PostGIS not
          // configured, or no name for this code — every branch falls back
          // to the bare code, exactly the label used before names existed.
          const count = `${item.total.toLocaleString("id-ID")} ${countNoun}`;
          if (item.name && keepCode) {
            opt.textContent = `${labelPrefix}${item.code} (${item.name}, ${count})`;
          } else if (item.name) {
            opt.textContent = `${labelPrefix}${item.name} (${count})`;
          } else {
            opt.textContent = `${labelPrefix}${item.code} (${count})`;
          }
          selectEl.appendChild(opt);
        }
      } catch (err) {
        console.error(`failed to load ${placeholder} list`, err);
      }
    }
    return { reset, load };
  }

  function esc(s) {
    if (s === null || s === undefined || s === "") return "-";
    return String(s).replace(/[&<>"']/g, (c) => ({
      "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
    }[c]));
  }

  // A dropdown you can pick several values in. Built by hand rather than
  // using <select multiple>, which browsers render as an always-expanded
  // list box — that would turn three one-line filter rows into three tall
  // scrolling panes and wreck the filter grid.
  //
  // rootEl is an empty <div class="multiselect"> carrying data-placeholder
  // (the "nothing picked" label) and data-labelledby (the id of its visible
  // label, for screen readers). Options arrive later via setOptions,
  // because they come from GET /api/filter-options.
  //
  // Returns { setOptions, getValues, reset }. Nothing here reloads data on
  // its own: like every other filter in this app, the picks only take
  // effect when Terapkan Filter is clicked, so the caller reads getValues()
  // at that moment.
  function makeMultiSelect(rootEl) {
    const placeholder = rootEl.dataset.placeholder || "Semua";
    const selected = new Set();

    const toggle = document.createElement("button");
    toggle.type = "button";
    toggle.className = "multiselect-toggle";
    toggle.setAttribute("aria-haspopup", "true");
    toggle.setAttribute("aria-expanded", "false");
    if (rootEl.dataset.labelledby) {
      // The button is the control, so point assistive tech at the row's
      // label plus the current value, the way a <select> would read.
      toggle.setAttribute("aria-labelledby", `${rootEl.dataset.labelledby} ${rootEl.id}-value`);
    }

    const valueEl = document.createElement("span");
    valueEl.className = "multiselect-value";
    valueEl.id = `${rootEl.id}-value`;
    const caret = document.createElement("span");
    caret.className = "multiselect-caret";
    caret.setAttribute("aria-hidden", "true");
    caret.textContent = "▾";
    toggle.append(valueEl, caret);

    const menu = document.createElement("div");
    menu.className = "multiselect-menu hidden";

    rootEl.append(toggle, menu);

    function renderValue() {
      // One pick shows its own name; several show a count, because the
      // button is one line wide and a list of long status names would just
      // be truncated into uselessness.
      if (selected.size === 0) {
        valueEl.textContent = placeholder;
        valueEl.classList.add("is-placeholder");
      } else if (selected.size === 1) {
        const only = [...selected][0];
        valueEl.textContent = only === EMPTY_VALUE ? "(Kosong)" : only;
        valueEl.classList.remove("is-placeholder");
      } else {
        valueEl.textContent = `${selected.size} dipilih`;
        valueEl.classList.remove("is-placeholder");
      }
      // The full list is always reachable on hover even when summarised.
      toggle.title = selected.size
        ? [...selected].map((v) => (v === EMPTY_VALUE ? "(Kosong)" : v)).join(", ")
        : placeholder;
    }

    function open() {
      menu.classList.remove("hidden");
      toggle.setAttribute("aria-expanded", "true");
    }
    function close() {
      menu.classList.add("hidden");
      toggle.setAttribute("aria-expanded", "false");
    }
    function isOpen() {
      return !menu.classList.contains("hidden");
    }

    toggle.addEventListener("click", () => (isOpen() ? close() : open()));

    // Clicking anywhere else closes it, the way a real dropdown behaves.
    document.addEventListener("click", (e) => {
      if (isOpen() && !rootEl.contains(e.target)) close();
    });
    rootEl.addEventListener("keydown", (e) => {
      if (e.key === "Escape" && isOpen()) {
        close();
        toggle.focus();
      }
    });

    function setOptions(values) {
      selected.clear();
      menu.innerHTML = "";

      const clearBtn = document.createElement("button");
      clearBtn.type = "button";
      clearBtn.className = "multiselect-clear";
      clearBtn.textContent = "Kosongkan pilihan";
      clearBtn.addEventListener("click", () => {
        selected.clear();
        menu.querySelectorAll("input[type=checkbox]").forEach((c) => (c.checked = false));
        renderValue();
      });
      menu.appendChild(clearBtn);

      // EMPTY_VALUE last, matching the old single-select order, so
      // "(Kosong)" reads as the odd one out rather than a real category.
      for (const v of [...values, EMPTY_VALUE]) {
        const row = document.createElement("label");
        row.className = "multiselect-option";
        const box = document.createElement("input");
        box.type = "checkbox";
        box.value = v;
        box.addEventListener("change", () => {
          if (box.checked) selected.add(v);
          else selected.delete(v);
          renderValue();
        });
        const text = document.createElement("span");
        text.textContent = v === EMPTY_VALUE ? "(Kosong)" : v;
        row.append(box, text);
        menu.appendChild(row);
      }
      renderValue();
    }

    function reset() {
      selected.clear();
      menu.querySelectorAll("input[type=checkbox]").forEach((c) => (c.checked = false));
      renderValue();
      close();
    }

    // Restores a saved set of picks (see filterPresets below). Values the
    // current option list doesn't contain are dropped rather than kept as
    // ghosts: the options come from the server's enum whitelist, so a
    // preset saved before that enum changed would otherwise send a value
    // the server now rejects with 400.
    function setValues(values) {
      const wanted = new Set(Array.isArray(values) ? values : []);
      selected.clear();
      menu.querySelectorAll("input[type=checkbox]").forEach((box) => {
        box.checked = wanted.has(box.value);
        if (box.checked) selected.add(box.value);
      });
      renderValue();
    }

    renderValue();
    return { setOptions, getValues: () => [...selected], setValues, reset };
  }

  // --- saved filter presets ---------------------------------------------
  // Named filter combinations, kept in localStorage. One shared store for
  // the Peta and Daftar menus: both filter on exactly the same fields, so
  // a preset saved while reading the table is just as valid on the map,
  // and two separate lists would only make the user save twice.
  //
  // Every access is wrapped. localStorage throws outright in some
  // configurations (Safari private browsing, "block all cookies", an
  // enterprise policy), and a filter panel that cannot save a preset
  // should still filter. A failed read reads as "no presets"; a failed
  // write is reported back so the caller can say so instead of pretending
  // the preset was kept.
  const PRESET_KEY = "se2026.filterPresets.v1";
  // A guard against an unbounded list, not a considered UX limit: the
  // dropdown stops being useful long before this, and localStorage has a
  // per-origin quota a runaway caller could otherwise fill.
  const PRESET_LIMIT = 50;

  function readPresets() {
    try {
      const raw = localStorage.getItem(PRESET_KEY);
      if (!raw) return [];
      const parsed = JSON.parse(raw);
      if (!parsed || !Array.isArray(parsed.presets)) return [];
      // Written by an older version, hand-edited, or half-overwritten:
      // keep whatever still looks like a preset and drop the rest.
      return parsed.presets.filter(
        (x) => x && typeof x.name === "string" && x.name !== "" && x.filter && typeof x.filter === "object"
      );
    } catch (err) {
      console.warn("filter presets unreadable, treating as empty", err);
      return [];
    }
  }

  function writePresets(presets) {
    try {
      localStorage.setItem(PRESET_KEY, JSON.stringify({ v: 1, presets }));
      return true;
    } catch (err) {
      console.warn("failed to save filter presets", err);
      return false;
    }
  }

  const filterPresets = {
    list: readPresets,
    get(name) {
      return readPresets().find((x) => x.name === name) || null;
    },
    // Saving under an existing name overwrites it — that is the "update
    // this preset" gesture, and the caller confirms before calling.
    save(name, filter) {
      const presets = readPresets().filter((x) => x.name !== name);
      if (presets.length >= PRESET_LIMIT) return { ok: false, reason: "limit" };
      presets.push({ name, filter, saved: new Date().toISOString() });
      presets.sort((a, b) => a.name.localeCompare(b.name, "id"));
      return { ok: writePresets(presets), reason: "storage" };
    },
    remove(name) {
      return writePresets(readPresets().filter((x) => x.name !== name));
    },
  };

  // Wires one preset bar to the store above. read() returns the menu's
  // current filter as a plain object and apply(filter) puts one back into
  // the controls and runs it — both stay with the menu, since only it
  // knows its own elements and its own cascading dropdowns.
  //
  // No prompt()/confirm(): those are blocked outright in embedded and
  // sandboxed contexts (measured — this app's own preview pane throws
  // "prompt() is not supported"), which would leave the save button doing
  // nothing at all. The name field and the delete confirmation are built
  // here instead, so both work wherever the page does.
  function makePresetBar({ rootEl, selectEl, saveBtn, deleteBtn, statusEl, read, apply }) {
    const form = document.createElement("div");
    form.className = "preset-name-form hidden";
    const input = document.createElement("input");
    input.type = "text";
    input.className = "preset-name-input";
    input.placeholder = "Nama preset…";
    input.setAttribute("aria-label", "Nama preset");
    const okBtn = document.createElement("button");
    okBtn.type = "button";
    okBtn.className = "preset-btn preset-btn-primary";
    okBtn.textContent = "Simpan";
    const cancelBtn = document.createElement("button");
    cancelBtn.type = "button";
    cancelBtn.className = "preset-btn";
    cancelBtn.textContent = "Batal";
    form.append(input, okBtn, cancelBtn);
    rootEl.appendChild(form);

    function say(msg) {
      if (statusEl) statusEl.textContent = msg || "";
    }

    function refresh(selectName) {
      const presets = filterPresets.list();
      selectEl.innerHTML = "";
      const head = document.createElement("option");
      head.value = "";
      head.textContent = presets.length ? "Pilih preset…" : "Belum ada preset";
      selectEl.appendChild(head);
      for (const preset of presets) {
        const opt = document.createElement("option");
        opt.value = preset.name;
        opt.textContent = preset.name;
        selectEl.appendChild(opt);
      }
      selectEl.value = presets.some((x) => x.name === selectName) ? selectName : "";
      selectEl.disabled = presets.length === 0;
      deleteBtn.disabled = selectEl.value === "";
    }

    // --- delete: armed by the first click, done by the second -----------
    // Deleting a preset can't be undone, so it needs a confirmation step;
    // a two-state button gives one without a dialog. It disarms itself
    // after a few seconds and whenever the selection changes, so a stray
    // click much later can't land on a primed button.
    let deleteTimer = 0;
    function disarmDelete() {
      clearTimeout(deleteTimer);
      deleteTimer = 0;
      deleteBtn.textContent = "Hapus";
      deleteBtn.classList.remove("is-armed");
    }

    deleteBtn.addEventListener("click", () => {
      const name = selectEl.value;
      if (!name) return;
      if (!deleteTimer) {
        deleteBtn.textContent = "Yakin hapus?";
        deleteBtn.classList.add("is-armed");
        say(`Klik sekali lagi untuk menghapus preset "${name}".`);
        deleteTimer = setTimeout(() => {
          disarmDelete();
          say("");
        }, 5000);
        return;
      }
      disarmDelete();
      const ok = filterPresets.remove(name);
      refresh("");
      say(ok ? `Preset "${name}" dihapus.` : "Gagal menghapus preset.");
    });

    // --- save -----------------------------------------------------------
    function openForm() {
      disarmDelete();
      input.value = selectEl.value || "";
      form.classList.remove("hidden");
      input.focus();
      input.select();
      say("");
    }
    function closeForm() {
      form.classList.add("hidden");
    }

    function commitSave() {
      const name = input.value.trim();
      if (!name) {
        say("Nama preset tidak boleh kosong.");
        input.focus();
        return;
      }
      // Saving under a name that already exists updates it. The name was
      // typed out in full, so that is nearly always what was meant — the
      // message afterwards says "diperbarui" rather than "tersimpan" so it
      // is never a silent surprise.
      const existed = Boolean(filterPresets.get(name));
      const res = filterPresets.save(name, read());
      if (!res.ok) {
        say(res.reason === "limit"
          ? `Preset sudah mencapai batas ${PRESET_LIMIT}. Hapus salah satu dulu.`
          : "Gagal menyimpan preset — penyimpanan browser tidak tersedia.");
        return;
      }
      closeForm();
      refresh(name);
      say(existed ? `Preset "${name}" diperbarui.` : `Preset "${name}" tersimpan.`);
    }

    saveBtn.addEventListener("click", () => {
      if (form.classList.contains("hidden")) openForm();
      else closeForm();
    });
    okBtn.addEventListener("click", commitSave);
    cancelBtn.addEventListener("click", () => {
      closeForm();
      say("");
    });
    input.addEventListener("keydown", (e) => {
      if (e.key === "Enter") {
        e.preventDefault();
        commitSave();
      } else if (e.key === "Escape") {
        closeForm();
        say("");
      }
    });

    // --- pick one --------------------------------------------------------
    selectEl.addEventListener("change", async () => {
      disarmDelete();
      const name = selectEl.value;
      deleteBtn.disabled = name === "";
      if (!name) return;
      const preset = filterPresets.get(name);
      if (!preset) {
        // Deleted in another tab since this dropdown was last built.
        refresh("");
        say("Preset itu sudah tidak ada.");
        return;
      }
      say(`Menerapkan "${name}"…`);
      await apply(preset.filter);
      say(`Preset "${name}" diterapkan.`);
    });

    refresh("");
    return { refresh };
  }

  // Must match points.EmptyValue on the server — the sentinel meaning
  // "match rows where this column is blank", as opposed to not filtering.
  const EMPTY_VALUE = "__EMPTY__";

  return {
    fetchWithRetry, makeCascadingLevel, makeMultiSelect, makePresetBar,
    filterPresets, esc, EMPTY_VALUE,
  };
})();
