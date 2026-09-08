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

    renderValue();
    return { setOptions, getValues: () => [...selected], reset };
  }

  // Must match points.EmptyValue on the server — the sentinel meaning
  // "match rows where this column is blank", as opposed to not filtering.
  const EMPTY_VALUE = "__EMPTY__";

  return { fetchWithRetry, makeCascadingLevel, makeMultiSelect, esc, EMPTY_VALUE };
})();
