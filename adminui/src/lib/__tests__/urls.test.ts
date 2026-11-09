import { describe, expect, it } from "vitest";

import { adminURL } from "../urls";
import { setAdminUIRoot } from "../../test/adminRoot";

describe("adminURL", () => {
  it.each([
    { documentPath: "/", appRoot: "./", expectedRoot: "/" },
    { documentPath: "/ui", appRoot: "./", expectedRoot: "/" },
    { documentPath: "/ui/", appRoot: "../", expectedRoot: "/" },
    { documentPath: "/gateway/client/", appRoot: "./", expectedRoot: "/gateway/client/" },
    { documentPath: "/gateway/client/ui", appRoot: "./", expectedRoot: "/gateway/client/" },
    { documentPath: "/gateway/client/ui/", appRoot: "../", expectedRoot: "/gateway/client/" },
    { documentPath: "/gateway/client/assets/", appRoot: "../", expectedRoot: "/gateway/client/" },
  ])("resolves local routes from $documentPath", ({ documentPath, appRoot, expectedRoot }) => {
    setAdminUIRoot(`${documentPath}?view=compact#logs`, appRoot);

    expect(adminURL("/api/status")).toBe(`${expectedRoot}api/status`);
    expect(adminURL("/metrics")).toBe(`${expectedRoot}metrics`);
    expect(adminURL("/api/harpoon/calls?label=a%2Fb&limit=200#details")).toBe(
      `${expectedRoot}api/harpoon/calls?label=a%2Fb&limit=200#details`,
    );
  });

  it("uses the current directory when root metadata is absent", () => {
    expect(adminURL("/api/status")).toBe("/api/status");
  });

  it.each([
    "https://other.example/api/status",
    "//other.example/api/status",
    "/\\other.example/api/status",
    "/../outside",
    "/api/../../outside",
    "/%2e%2e/outside",
    "/api/%2f..%2foutside",
    "/api/%5coutside",
  ])("rejects a route outside the local application: %s", (path) => {
    setAdminUIRoot("/gateway/client/", "./");

    expect(() => adminURL(path)).toThrow();
  });

  it.each(["https://other.example/", "./?query=1", "./#fragment", "ui"])(
    "rejects invalid application root metadata: %s",
    (appRoot) => {
      setAdminUIRoot("/gateway/client/", appRoot);

      expect(() => adminURL("/api/status")).toThrow("invalid admin UI application root");
    },
  );
});
