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
