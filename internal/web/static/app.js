// Small progressive enhancements; every page works without this file.
document.addEventListener("click", async (e) => {
  const btn = e.target.closest("button.copy");
  if (!btn) return;
  try {
    await navigator.clipboard.writeText(btn.dataset.copy);
    btn.classList.add("copied");
    setTimeout(() => btn.classList.remove("copied"), 1200);
  } catch {
    window.prompt("Copy item name", btn.dataset.copy);
  }
});

document.addEventListener("change", (e) => {
  if (e.target.matches("select.autosubmit")) e.target.form.submit();
});

// Keep expanded sets open across the page's auto-refresh.
const openKey = "open:" + location.pathname;
const openIDs = new Set(JSON.parse(sessionStorage.getItem(openKey) || "[]"));
for (const d of document.querySelectorAll("details[id]")) {
  if (openIDs.has(d.id)) d.open = true;
}
// toggle doesn't bubble, so listen in the capture phase.
document.addEventListener("toggle", (e) => {
  const d = e.target;
  if (!d.matches?.("details[id]")) return;
  d.open ? openIDs.add(d.id) : openIDs.delete(d.id);
  sessionStorage.setItem(openKey, JSON.stringify([...openIDs]));
}, true);
