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

  function status(msg) {
    last = msg;
    try { window.dispatchEvent(new CustomEvent("wctr-compose", { detail: msg })); } catch (_) { /* ignored */ }
  }

  async function importFiles(list) {
    const files = Array.from(list || []);
    if (files.length === 0) return;
    if (files.length > MAX_FILES) return status("import: more than " + MAX_FILES + " files");
    let total = 0;
    for (const f of files) {
      if (f.size > MAX_FILE) return status("import: " + f.name + " is over " + MAX_FILE + " bytes");
      total += f.size;
    }
    if (total > MAX_TOTAL) return status("import: over " + MAX_TOTAL + " bytes in all");
    const names = [];
    const texts = [];
    for (const f of files) {
      names.push(f.name);
      texts.push(await f.text());
    }
    if (typeof window.__wctrComposeImport !== "function") return status("import: the dashboard is not ready");
    const err = window.__wctrComposeImport(names, texts);
    status(err ? err : "imported " + names.length + " file(s)");
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
    if (typeof window.__wctrComposeExport !== "function") return status("export: the dashboard is not ready");
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
      status(String(err));
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
