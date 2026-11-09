// Resolve application-local routes without depending on a proxy's mount path.
// The HTML entrypoint provides a relative path to the application root.
export function adminURL(path: string): string {
  const routePath = path.split(/[?#]/, 1)[0];
  if (
    !routePath.startsWith("/") ||
    routePath.startsWith("//") ||
    /\\|%(?:2f|5c)/i.test(routePath)
  ) {
    throw new Error("admin UI route must be a local path");
  }

  const documentURL = new URL(window.location.href);
  const appRoot =
    document.querySelector<HTMLMetaElement>('meta[name="admin-ui-root"]')?.content ?? "./";
  const rootURL = new URL(appRoot, documentURL);
  if (
    rootURL.origin !== documentURL.origin ||
    !rootURL.pathname.endsWith("/") ||
    rootURL.search ||
    rootURL.hash
  ) {
    throw new Error("invalid admin UI application root");
  }

  const url = new URL(`.${path}`, rootURL);
  if (url.origin !== rootURL.origin || !url.pathname.startsWith(rootURL.pathname)) {
    throw new Error("admin UI route escapes the application root");
  }
  return `${url.pathname}${url.search}${url.hash}`;
}
