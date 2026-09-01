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
  function makeCascadingLevel({ selectEl, placeholder, byCode, labelPrefix }) {
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
          // (kecamatan/desa only, for now) — fall back to the bare code
          // when it isn't set, same label as before this existed.
          const label = item.name ? item.name : item.code;
          opt.textContent = `${labelPrefix}${label} (${item.total.toLocaleString("id-ID")} titik)`;
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

  return { fetchWithRetry, makeCascadingLevel, esc };
})();
