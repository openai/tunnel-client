"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");

const {
  buildConnectArgs,
  buildCreateArgs,
  buildListArgs,
  validateControlPlaneOverride,
} = require("./server.cjs");

test("allows the supported OpenAI control-plane origins", () => {
  assert.doesNotThrow(() => validateControlPlaneOverride("https://api.openai.com"));
  assert.doesNotThrow(() => validateControlPlaneOverride("https://mtls.api.openai.com"));
});

test("rejects attacker-controlled control-plane origins", () => {
  assert.throws(
    () => validateControlPlaneOverride("https://attacker.example"),
    /control_plane_base_url must be https:\/\/api\.openai\.com/,
  );
  assert.throws(
    () => validateControlPlaneOverride("https://api.openai.com.attacker.example"),
    /control_plane_base_url must be https:\/\/api\.openai\.com/,
  );
});

test("rejects URL components that can retarget an official origin", () => {
  for (const raw of [
    "http://api.openai.com",
    "https://user:secret@api.openai.com",
    "https://api.openai.com/v1/tunnels",
    "https://api.openai.com?target=attacker",
    "https://api.openai.com#fragment",
  ]) {
    assert.throws(
      () => validateControlPlaneOverride(raw),
      /control_plane_base_url must be an HTTPS origin/,
      raw,
    );
  }
});

test("lifecycle tool argument builders reject an untrusted override before spawning", () => {
  assert.throws(
    () => buildCreateArgs({
      alias: "demo",
      organization_id: "org_123",
      control_plane_base_url: "https://attacker.example",
    }),
    /control_plane_base_url/,
  );
  assert.throws(
    () => buildConnectArgs({
      alias: "demo",
      tunnel_id: "tunnel_123",
      mcp_command: "node server.js",
      runtime_api_key: "file:/tmp/secret",
      control_plane_base_url: "https://attacker.example",
    }),
    /control_plane_base_url/,
  );
  assert.throws(
    () => buildListArgs({
      organization_id: "org_123",
      control_plane_base_url: "https://attacker.example",
    }),
    /control_plane_base_url/,
  );
});

test("trusted environments can explicitly opt into a custom HTTPS origin", () => {
  const original = process.env.TUNNEL_MCP_ALLOW_CUSTOM_CONTROL_PLANE;
  process.env.TUNNEL_MCP_ALLOW_CUSTOM_CONTROL_PLANE = "1";
  try {
    assert.doesNotThrow(() => validateControlPlaneOverride("https://staging.example.test"));
    assert.throws(
      () => validateControlPlaneOverride("http://staging.example.test"),
      /control_plane_base_url must be an HTTPS origin/,
    );
  } finally {
    if (original === undefined) {
      delete process.env.TUNNEL_MCP_ALLOW_CUSTOM_CONTROL_PLANE;
    } else {
      process.env.TUNNEL_MCP_ALLOW_CUSTOM_CONTROL_PLANE = original;
    }
  }
});
