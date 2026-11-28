"use strict";
// compose-io.js: the page's side of the Topology view's Import and Export
// compose controls. The dashboard (Go, wasm) calls window.__wctrCompose:
//
//   pick()          opens a file picker (compose files, .env, includes); the
//                   chosen files are read as text, bounded, and handed to the
//                   Go side (window.__wctrComposeImport), which validates the
//                   names and sizes and keeps them in the session's one
//                   compose FS: the same one `wctr compose` reads.
//   export(project) asks Go for the project's resolved compose text
//                   (window.__wctrComposeExport) and saves it as
//                   <project>.compose.yaml.
//
// Dropping files on the page does the same as pick(). Nothing is persisted.
(function () {
  const MAX_FILES = 64;
  const MAX_FILE = 4 << 20;
  const MAX_TOTAL = 16 << 20;

  let last = ""; // the last outcome, for the e2e and the status line

  // status records the outcome and shows it: the dashboard's Topology view
  // renders refusals and errors (window.__wctrComposeFailed); Go reports its
  // own outcomes there directly. ok is false for a refusal or an error.
  function status(msg, ok) {
    last = msg;
    if (ok === false && typeof window.__wctrComposeFailed === "function") {
      try { window.__wctrComposeFailed(msg); } catch (_) { /* the view is not up */ }
    }
    try { window.dispatchEvent(new CustomEvent("wctr-compose", { detail: msg })); } catch (_) { /* ignored */ }
  }
  const refuse = (msg) => status(msg, false);

  // confirmReplace: a drop (or a pick) over files already held replaces them
  // whole, so it asks first. Nothing is read or sent before the answer.
  function confirmReplace(n) {
    const has = typeof window.__wctrComposeHas === "function" && window.__wctrComposeHas();
    if (!has) return true;
    return window.confirm("Replace the compose files already imported with " + n + " new file(s)?");
  }

  async function importFiles(list) {
    const files = Array.from(list || []);
    if (files.length === 0) return;
    if (files.length > MAX_FILES) return refuse("import: more than " + MAX_FILES + " files");
    let total = 0;
    for (const f of files) {
      if (f.size > MAX_FILE) return refuse("import: " + f.name + " is over " + MAX_FILE + " bytes");
      total += f.size;
    }
    if (total > MAX_TOTAL) return refuse("import: over " + MAX_TOTAL + " bytes in all");
    if (typeof window.__wctrComposeImport !== "function") return refuse("import: the dashboard is not ready");
    if (!confirmReplace(files.length)) return refuse("import: cancelled, the files already imported are kept");
    const names = [];
    const texts = [];
    for (const f of files) {
      names.push(f.name);
      texts.push(await f.text());
    }
    const err = await window.__wctrComposeImport(names, texts);
    status(err ? err : "imported " + names.length + " file(s)", !err);
  }

  function pick() {
    const input = document.createElement("input");
    input.type = "file";
    input.multiple = true;
    input.accept = ".yaml,.yml,.env,text/yaml,text/plain";
    input.style.display = "none";
    input.addEventListener("change", () => {
      importFiles(input.files).finally(() => input.remove());
    });
    document.body.appendChild(input);
    input.click();
  }

  function safeName(project) {
    return String(project).replace(/[^a-z0-9_-]/g, "_").slice(0, 64) || "project";
  }

  async function exportProject(project) {
    if (typeof window.__wctrComposeExport !== "function") return refuse("export: the dashboard is not ready");
    try {
      const text = await window.__wctrComposeExport(String(project));
      const a = document.createElement("a");
      a.href = URL.createObjectURL(new Blob([text], { type: "application/yaml" }));
      a.download = safeName(project) + ".compose.yaml";
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(a.href), 60000);
      status("exported " + a.download);
    } catch (err) {
      // Go has already reported the refusal to the view; keep the page's record.
      last = String(err);
      try { window.dispatchEvent(new CustomEvent("wctr-compose", { detail: last })); } catch (_) { /* ignored */ }
    }
  }

  window.addEventListener("dragover", (e) => {
    if (e.dataTransfer && Array.from(e.dataTransfer.types || []).includes("Files")) e.preventDefault();
  });
  window.addEventListener("drop", (e) => {
    if (!e.dataTransfer || !e.dataTransfer.files || e.dataTransfer.files.length === 0) return;
    e.preventDefault();
    importFiles(e.dataTransfer.files);
  });

  window.__wctrCompose = { pick, export: exportProject };
  window.__wctrComposeLast = () => last;
})();
