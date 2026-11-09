export function setAdminUIRoot(documentPath: string, appRoot: string): void {
  window.history.replaceState(null, "", documentPath);
  let meta = document.querySelector<HTMLMetaElement>('meta[name="admin-ui-root"]');
  if (!meta) {
    meta = document.createElement("meta");
    meta.name = "admin-ui-root";
    document.head.append(meta);
  }
  meta.content = appRoot;
}

export function resetAdminUIRoot(): void {
  document.querySelector('meta[name="admin-ui-root"]')?.remove();
  window.history.replaceState(null, "", "/");
}
