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
  function makeCascadingLevel({ selectEl, placeholder, byCode, labelPrefix, keepCode = false }) {
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
          const count = `${item.total.toLocaleString("id-ID")} titik`;
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

  return { fetchWithRetry, makeCascadingLevel, esc };
})();
