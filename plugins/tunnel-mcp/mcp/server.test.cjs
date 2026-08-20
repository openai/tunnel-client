"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");

const {
  buildConnectArgs,
  buildCreateArgs,
  buildListArgs,
  handleRpc,
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
    "https:api.openai.com",
    "https:/api.openai.com",
    "https://user:secret@api.openai.com",
    "https://api.openai.com/v1/tunnels",
    "https://api.openai.com?target=attacker",
    "https://api.openai.com#fragment",
    "ftp://api.openai.com",
  ]) {
    assert.throws(
      () => validateControlPlaneOverride(raw),
      /control_plane_base_url must be an HTTP or HTTPS origin/,
      raw,
    );
  }
});

test("rejects an HTTP downgrade of an official control-plane origin", () => {
  assert.throws(
    () => validateControlPlaneOverride("http://api.openai.com"),
    /control_plane_base_url must be https:\/\/api\.openai\.com/,
  );
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

test("trusted CONTROL_PLANE_BASE_URL allows only its exact configured origin", () => {
  const envName = "CONTROL_PLANE_BASE_URL";
  const original = process.env[envName];
  try {
    process.env[envName] = "https://staging.example.test/";
    assert.doesNotThrow(() => validateControlPlaneOverride("https://staging.example.test"));
    assert.throws(
      () => validateControlPlaneOverride("https://attacker.example"),
      /control_plane_base_url must be https:\/\/api\.openai\.com/,
    );
    assert.throws(
      () => validateControlPlaneOverride("http://staging.example.test"),
      /control_plane_base_url must be https:\/\/api\.openai\.com/,
    );

    process.env[envName] = "http://localhost:8080/";
    assert.doesNotThrow(() => validateControlPlaneOverride("http://localhost:8080"));
    assert.throws(
      () => validateControlPlaneOverride("http://localhost:8081"),
      /control_plane_base_url must be https:\/\/api\.openai\.com/,
    );
  } finally {
    if (original === undefined) {
      delete process.env[envName];
    } else {
      process.env[envName] = original;
    }
  }
});

test("rejects inline secret values in stdio commands before spawning", () => {
  for (const command of [
    "node server.js --api-key sk-test-veracode-sentinel",
    "node server.js -api-key plain-text-sentinel",
    "node server.js --aws-secret-access-key plain-text-aws-flag-secret",
    "node server.js --client-secret-value plain-text-client-flag-secret",
    "node server.js --admin-key plain-text-admin-flag-secret",
    "node server.js --webhook-secret plain-text-webhook-secret",
    "node server.js --token-file plain-text-token-file-secret",
    "node server.js --secret-path plain-text-secret-path",
    "OPENAI_API_KEY=plain-text-secret node server.js",
    "OPENAI_ADMIN_KEY=plain-text-admin-secret node server.js",
    "SESSION_SECRET=plain-text-session-secret node server.js",
    "TOKEN_FILE=plain-text-token-file-secret node server.js",
    "AWS_SECRET_ACCESS_KEY=plain-text-aws-secret node server.js",
    "node server.js --header X-Api-Key=plain-text-header-secret",
    "node server.js https://example.com?api-key=plain-text-query-secret",
    "AUTHORIZATION=plain-text-authorization node server.js",
    "CREDENTIAL=plain-text-credential node server.js",
    "PRIVATE_KEY=plain-text-private-key node server.js",
    "DB_PASS=PLA605_PASSWORD_ALIAS node server.js",
    "DB_PASSWD=PLA605_PASSWORD_ALIAS node server.js",
    "node server.js --passphrase PLA605_PASSWORD_ALIAS",
    "API_KEY_HEADER=PLA605_INLINE_HEADER_SECRET node server.js",
    "PASSWORD_HASH_ALGORITHM=PLA605_INLINE_ALGORITHM_SECRET node server.js",
    "PASSWORD_MIN=PLA605_MIN_LEAK node server.js",
    "COOKIE_DOMAIN=PLA605_DOMAIN_LEAK node server.js",
    "PASSWORD_POLICY=PLA605_POLICY_LEAK node server.js",
    "API_KEY_URL=https://example.test/?token=PLA605_URL_QUERY_LEAK node server.js",
    "API_KEY_URL=https://example.test/#access_token=PLA605_URL_FRAGMENT_LEAK node server.js",
    "API_KEY_URL=https://example.test/#token=PLA605_FRAGMENT_TOKEN_LEAK node server.js",
    "API_KEY_URL=https://example.test/#runtime_token=PLA605_RUNTIME_TOKEN_FRAGMENT_LEAK node server.js",
    "API_KEY_URL=https://example.test/#auth_token=PLA605_AUTH_TOKEN_FRAGMENT_LEAK node server.js",
    "API_KEY_URL=https://example.test/#private_key=PLA605_PRIVATE_KEY_FRAGMENT_LEAK node server.js",
    "API_KEY_URL=https://example.test/#secret=PLA605_SECRET_FRAGMENT_LEAK node server.js",
    "API_KEY_URL=https://example.test/#token%3DPLA605_ENCODED_SEPARATOR_LEAK node server.js",
    "api_key[]=PLA605_BRACKET_SECRET node server.js",
    "api_key[primary]=PLA605_BRACKET_PRIMARY_SECRET node server.js",
    "headers[Authorization]=PLA605_BRACKET_HEADER_SECRET node server.js",
    "node server.js --header 'Authorization: Bearer bearer-test-sentinel'",
    "node server.js --header 'Authorization: Basic plain-text-basic-sentinel'",
    "node server.js --header 'Cookie: session=cookie-test-sentinel'",
    "node server.js --config '{\"runtime_token\":{\"value\":\"abcd\"}}'",
    "node server.js https://user:password@example.com",
    "node server.js https://user@example.com",
    "node server.js https://env:PLA605_PASSWORD@example.com",
  ]) {
    assert.throws(
      () => buildConnectArgs({
        alias: "demo",
        tunnel_id: "tunnel_123",
        mcp_command: command,
      }),
      /mcp_command must not contain inline secret values/,
      command,
    );
  }

  assert.doesNotThrow(() => buildConnectArgs({
    alias: "demo",
    tunnel_id: "tunnel_123",
    mcp_command: "node server.js --api-key env:OPENAI_API_KEY",
  }));
  assert.doesNotThrow(() => buildConnectArgs({
    alias: "demo",
    tunnel_id: "tunnel_123",
    mcp_command: "node server.js --api-key file:/tmp/api-key",
  }));
  assert.doesNotThrow(() => buildConnectArgs({
    alias: "demo",
    tunnel_id: "tunnel_123",
    mcp_command: "node server.js --api-key $OPENAI_API_KEY",
  }));
  assert.doesNotThrow(() => buildConnectArgs({
    alias: "demo",
    tunnel_id: "tunnel_123",
    mcp_command: "node server.js --api-key $" + "{OPENAI_API_KEY}",
  }));
  assert.doesNotThrow(() => buildConnectArgs({
    alias: "demo",
    tunnel_id: "tunnel_123",
    mcp_command: "node server.js --api-key %OPENAI_API_KEY%",
  }));
  assert.doesNotThrow(() => buildConnectArgs({
    alias: "demo",
    tunnel_id: "tunnel_123",
    mcp_command: "node server.js --api-key $env:OPENAI_API_KEY",
  }));
  assert.doesNotThrow(() => buildConnectArgs({
    alias: "demo",
    tunnel_id: "tunnel_123",
    mcp_command: "GOOGLE_APPLICATION_CREDENTIALS=/run/secrets/gcp.json node server.js",
  }));
  assert.doesNotThrow(() => buildConnectArgs({
    alias: "demo",
    tunnel_id: "tunnel_123",
    mcp_command: "AWS_SHARED_CREDENTIALS_FILE=/home/me/.aws/credentials node server.js",
  }));
  assert.doesNotThrow(() => buildConnectArgs({
    alias: "demo",
    tunnel_id: "tunnel_123",
    mcp_command: "node server.js --token-file /run/secrets/token --credentials-file /run/config.json",
  }));
  assert.doesNotThrow(() => buildConnectArgs({
    alias: "demo",
    tunnel_id: "tunnel_123",
    mcp_command:
      "node server.js --credentials-file credentials.json --token-file token.txt --api-key-file key.pem",
  }));
  assert.doesNotThrow(() => buildConnectArgs({
    alias: "demo",
    tunnel_id: "tunnel_123",
    mcp_command: "node server.js --private-key /run/secrets/client.key",
  }));
  assert.doesNotThrow(() => buildConnectArgs({
    alias: "demo",
    tunnel_id: "tunnel_123",
    mcp_command: "node server.js --api-key-env OPENAI_API_KEY --api-key-name OPENAI_API_KEY",
  }));
  assert.doesNotThrow(() => buildConnectArgs({
    alias: "demo",
    tunnel_id: "tunnel_123",
    mcp_command: 'node server.js --header "X-Api-Key: $TOKEN" --header "Authorization: Bearer $TOKEN"',
  }));
  assert.doesNotThrow(() => buildConnectArgs({
    alias: "demo",
    tunnel_id: "tunnel_123",
    mcp_command: 'node server.js --header "Authorization: Basic $BASIC_AUTH"',
  }));
  assert.doesNotThrow(() => buildConnectArgs({
    alias: "demo",
    tunnel_id: "tunnel_123",
    mcp_command:
      "curl -H 'Authorization: Basic $BASIC_AUTH' -H 'Accept: application/json' https://example.test",
  }));
  assert.doesNotThrow(() => buildConnectArgs({
    alias: "demo",
    tunnel_id: "tunnel_123",
    mcp_command: 'OPENAI_API_KEY="" node server.js',
  }));
  assert.doesNotThrow(() => buildConnectArgs({
    alias: "demo",
    tunnel_id: "tunnel_123",
    mcp_command: "node server.js --config '{\"token\":\"\"}'",
  }));
  for (const command of [
    "AUTHORIZATION_URL=https://login.example.com/oauth node server.js",
    "API_KEY_URL=https://example.test/#token=env:OPENAI_API_KEY node server.js",
    "PASSWORD_MIN_LENGTH=12 node server.js",
    "COOKIE_DOMAIN=example.com node server.js",
    "COOKIE_DOMAIN=.example.com node server.js",
    "OPENAI_API_KEY_ENABLED=true node server.js",
    "API_KEY_HEADER=Authorization node server.js",
    "PASSWORD_HASH_ALGORITHM=argon2 node server.js",
  ]) {
    assert.doesNotThrow(() => buildConnectArgs({
      alias: "demo",
      tunnel_id: "tunnel_123",
      mcp_command: command,
    }));
  }
});

test("rejects the connect_stdio_mcp inline-secret repro before spawning", async () => {
  const sentinel = "PLA605_INLINE_SECRET_SENTINEL";
  const originalStateDir = process.env.TUNNEL_CLIENT_STATE_DIR;
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-no-spawn-"));
  const bin = path.join(dir, "tunnel-client");
  const marker = path.join(dir, "spawned");
  fs.writeFileSync(
    bin,
    [
      "#!/bin/sh",
      "touch '" + marker + "'",
      "exit 1",
      "",
    ].join("\n"),
    { mode: 0o700 },
  );

  try {
    process.env.TUNNEL_CLIENT_STATE_DIR = "/dev/null";
    const response = await handleRpc({
      method: "tools/call",
      id: 1,
      params: {
        name: "connect_stdio_mcp",
        arguments: {
          alias: "demo",
          tunnel_id: "tunnel_123",
          mcp_command: "node server.js --api-key " + sentinel,
          tunnel_client_bin: bin,
        },
      },
    });
    const encoded = JSON.stringify(response);
    assert.doesNotMatch(encoded, new RegExp(sentinel));
    assert.match(response.error.message, /mcp_command must not contain inline secret values/);
    assert.equal(response.error.data, undefined);
    assert.equal(fs.existsSync(marker), false);
  } finally {
    if (originalStateDir === undefined) {
      delete process.env.TUNNEL_CLIENT_STATE_DIR;
    } else {
      process.env.TUNNEL_CLIENT_STATE_DIR = originalStateDir;
    }
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("rejects inline secrets appended to runtime API key references", () => {
  for (const runtimeAPIKey of [
    "env:OPENAI_API_KEY sk-test-runtime-key-sentinel",
    "file:/tmp/runtime-key sk-test-runtime-key-sentinel",
  ]) {
    assert.throws(
      () => buildConnectArgs({
        alias: "demo",
        tunnel_id: "tunnel_123",
        mcp_command: "node server.js",
        runtime_api_key: runtimeAPIKey,
      }),
      /runtime_api_key must be a secret reference/,
      runtimeAPIKey,
    );
  }

  assert.doesNotThrow(() => buildConnectArgs({
    alias: "demo",
    tunnel_id: "tunnel_123",
    mcp_command: "node server.js",
    runtime_api_key: "env:OPENAI_API_KEY",
  }));
  assert.doesNotThrow(() => buildConnectArgs({
    alias: "demo",
    tunnel_id: "tunnel_123",
    mcp_command: "node server.js",
    runtime_api_key: "file:/tmp/runtime-key",
  }));
});

test("preserves env and file references through a failed native launch", async () => {
  const mcpCommand =
    "node server.js --api-key env:OPENAI_API_KEY --token file:/tmp/runtime-key";
  const runtimeAPIKey = "file:/tmp/control-plane-key";
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-secret-refs-"));
  const bin = path.join(dir, "tunnel-client");
  const marker = path.join(dir, "spawned");
  fs.writeFileSync(
    bin,
    [
      "#!/bin/sh",
      "touch '" + marker + "'",
      "printf '%s\\n' '{}'",
      "exit 1",
      "",
    ].join("\n"),
    { mode: 0o700 },
  );

  try {
    const response = await handleRpc({
      method: "tools/call",
      id: 1,
      params: {
        name: "connect_stdio_mcp",
        arguments: {
          alias: "demo",
          tunnel_id: "tunnel_123",
          mcp_command: mcpCommand,
          runtime_api_key: runtimeAPIKey,
          tunnel_client_bin: bin,
        },
      },
    });
    assert.equal(fs.existsSync(marker), true);
    assert.equal(response.error.data.command[6], "[REDACTED]");
    assert.ok(response.error.data.command.includes(runtimeAPIKey));
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("redacts secrets from native launch failure output", async () => {
  const sentinel = "sk-test-veracode-sentinel";
  const plainSentinel = "plain-text-native-secret";
  const escapedSentinel = "plain-text-escaped-secret";
  const localTailSentinel = "plain-text-local-tail-secret";
  const basicSentinel = "plain-text-basic-secret";
  const cookieSentinel = "plain-text-cookie-secret";
  const adminSentinel = "plain-text-admin-secret";
  const arraySentinel = "plain-text-array-secret";
  const objectSentinel = "plain-text-object-secret";
  const userinfoSentinel = "plain-text-userinfo-secret";
  const userinfoReference = "env:PLA605_PASSWORD";
  const redeemableTokenSentinel = "plain-text-redeemable-token-secret";
  const credentialsSentinel = "plain-text-credentials-secret";
  const privateKeySentinel = "plain-text-private-key-secret";
  const awsSecretSentinel = "plain-text-aws-secret-access-key";
  const newlineHeaderSentinel = "plain-text-newline-header-secret";
  const targetValueSentinel = "plain-text-target-value-secret";
  const numericPassword = 123456;
  const numericToken = 7890;
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-redaction-"));
  const bin = path.join(dir, "tunnel-client");
  fs.writeFileSync(
    bin,
    [
      "#!/bin/sh",
      "printf '%s\\n' '{\"api_key\":\"" + plainSentinel + "\",\"runtime_token\":[\"" + arraySentinel + "\"],\"admin_key\":{\"value\":\"" + objectSentinel + "\"},\"AWS_SECRET_ACCESS_KEY\":\"" + awsSecretSentinel + "\",\"password\":" + numericPassword + ",\"token\":" + numericToken + ",\"x-api-key\":\"" + plainSentinel + "\",\"command\":[\"node\",\"--api-key\",\"" + plainSentinel + "\"],\"process\":{\"command\":\"node server.js --token " + sentinel + "\",\"target_kind\":\"command\",\"target_value\":\"node server.js " + targetValueSentinel + "\"},\"launch_diagnostics\":{\"log_tail\":\"OPENAI_API_KEY=" + sentinel + "\"},\"local\":{\"log\":{\"tail\":\"Authorization: Bearer " + localTailSentinel + "\"}}}'",
      "printf '%s\\n' 'Bearer " + sentinel + " {\\\"api_key\\\":\\\"" + escapedSentinel + "\\\"}' >&2",
      "printf '%s\\n' 'Authorization: Basic " + basicSentinel + " Cookie: a=first; b=" + cookieSentinel + " X-OAI-BRTE-Redeemable-Token: " + redeemableTokenSentinel + " {\"admin_key\":\"" + adminSentinel + "\",\"credentials\":\"" + credentialsSentinel + "\",\"private_key\":\"" + privateKeySentinel + "\"}' >&2",
      "printf '%s\\n' 'error: X-Api-Key: " + newlineHeaderSentinel + "' >&2",
      "printf '%s\\n' 'next diagnostic line' >&2",
      "printf '%s\\n' 'https://" + userinfoSentinel + "@example.com/path' >&2",
      "printf '%s\\n' 'https://" + userinfoReference + "@example.com/path' >&2",
      "exit 1",
      "",
    ].join("\n"),
    { mode: 0o700 },
  );

  try {
    const response = await handleRpc({
      method: "tools/call",
      id: 1,
      params: {
        name: "runtime_status",
        arguments: { alias: "demo", tunnel_client_bin: bin },
      },
    });
    const encoded = JSON.stringify(response);
    assert.ok(response.error.data.command.includes("status"));
    assert.equal(response.error.data.native.command[2], "[REDACTED]");
    assert.equal(response.error.data.native.process.command, "node server.js --token [REDACTED]");
    assert.equal(response.error.data.native.process.target_value, "[REDACTED]");
    assert.match(response.error.data.stderr, /\[REDACTED\]/);
    assert.match(response.error.data.native.launch_diagnostics.log_tail, /\[REDACTED\]/);
    assert.match(response.error.data.launch_diagnostics.log_tail, /\[REDACTED\]/);
    assert.ok(Array.isArray(response.error.data.native.runtime_token));
    assert.equal(typeof response.error.data.native.admin_key, "object");
    assert.equal(response.error.data.native.AWS_SECRET_ACCESS_KEY, "[REDACTED]");
    assert.equal(response.error.data.native.password, "[REDACTED]");
    assert.equal(response.error.data.native.token, "[REDACTED]");
    assert.doesNotMatch(encoded, new RegExp(sentinel));
    assert.doesNotMatch(encoded, new RegExp(plainSentinel));
    assert.doesNotMatch(encoded, new RegExp(escapedSentinel));
    assert.doesNotMatch(encoded, new RegExp(localTailSentinel));
    assert.doesNotMatch(encoded, new RegExp(basicSentinel));
    assert.doesNotMatch(encoded, new RegExp(cookieSentinel));
    assert.doesNotMatch(encoded, new RegExp(adminSentinel));
    assert.doesNotMatch(encoded, new RegExp(arraySentinel));
    assert.doesNotMatch(encoded, new RegExp(objectSentinel));
    assert.doesNotMatch(encoded, new RegExp(userinfoSentinel));
    assert.doesNotMatch(encoded, /PLA605_PASSWORD/);
    assert.doesNotMatch(encoded, new RegExp(redeemableTokenSentinel));
    assert.doesNotMatch(encoded, new RegExp(credentialsSentinel));
    assert.doesNotMatch(encoded, new RegExp(privateKeySentinel));
    assert.doesNotMatch(encoded, new RegExp(awsSecretSentinel));
    assert.doesNotMatch(encoded, new RegExp(newlineHeaderSentinel));
    assert.doesNotMatch(encoded, new RegExp(targetValueSentinel));
    assert.doesNotMatch(encoded, new RegExp(String(numericPassword)));
    assert.doesNotMatch(encoded, new RegExp(String(numericToken)));
    assert.match(encoded, /\[REDACTED\]/);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("redacts secret-shaped errors at the JSON-RPC output boundary", async () => {
  const sentinel = "sk-test-boundary-sentinel";
  const response = await handleRpc({
    method: "missing-" + sentinel,
    id: 1,
  });
  const encoded = JSON.stringify(response);
  assert.doesNotMatch(encoded, new RegExp(sentinel));
  assert.match(encoded, /\[REDACTED\]/);
});

test("redacts compound secret names in raw JSON diagnostics", async () => {
  const sentinel = "plain-text-raw-aws-secret";
  const response = await handleRpc({
    method: "missing-" + JSON.stringify({ AWS_SECRET_ACCESS_KEY: sentinel }),
    id: 1,
  });
  const encoded = JSON.stringify(response);
  assert.doesNotMatch(encoded, new RegExp(sentinel));
  assert.match(encoded, /\[REDACTED\]/);
});

test("collects leaves from embedded sensitive JSON subtrees", async () => {
  const sentinel = "PLA605_EMBEDDED_SUBTREE_LEAF";
  const response = await handleRpc({
    method:
      "missing-" +
      JSON.stringify({
        echo: sentinel,
        runtime_token: { value: sentinel },
      }),
    id: 1,
  });
  assert.doesNotMatch(JSON.stringify(response), new RegExp(sentinel));
});

test("resynchronizes on sensitive JSON after malformed diagnostic text", async () => {
  const sentinel = "PLA605_MALFORMED_SUBTREE_LEAK";
  const response = await handleRpc({
    method:
      "missing-{broken " +
      JSON.stringify({
        runtime_token: { value: sentinel },
      }),
    id: 1,
  });
  assert.doesNotMatch(JSON.stringify(response), new RegExp(sentinel));
});

test("fails closed on incomplete sensitive JSON containers", async () => {
  const sentinel = "PLA605_INCOMPLETE_CONTAINER_LEAK";
  const response = await handleRpc({
    method: 'missing-{\"runtime_token\":{\"value\":\"' + sentinel + '\"',
    id: 1,
  });
  assert.doesNotMatch(JSON.stringify(response), new RegExp(sentinel));
  assert.equal(response.error.message, "[REDACTED]");
});

test("redacts camelCase secret names in raw JSON diagnostics", async () => {
  const sentinel = "PLA605_OPENAI_ADMIN_CAMEL_LEAK";
  const response = await handleRpc({
    method: "missing-" + JSON.stringify({ openaiAdminKey: sentinel }),
    id: 1,
  });
  assert.doesNotMatch(JSON.stringify(response), new RegExp(sentinel));
});

test("redacts secret suffixes and dotted textual keys", async () => {
  const sessionSentinel = "PLA605_SESSION_SECRET_LEAK";
  const dottedSentinel = "PLA605_DOTTED_YAML_LEAK";
  const response = await handleRpc({
    method:
      "missing-" +
      JSON.stringify({ session_secret: sessionSentinel }) +
      " auth.token: " +
      dottedSentinel,
    id: 1,
  });
  const encoded = JSON.stringify(response);
  assert.doesNotMatch(encoded, new RegExp(sessionSentinel));
  assert.doesNotMatch(encoded, new RegExp(dottedSentinel));
});

test("redacts JSON secret values containing escaped quotes", async () => {
  const tailSentinel = "TAILLEAK";
  const response = await handleRpc({
    method: "missing-" + JSON.stringify({ token: "secret\"" + tailSentinel }),
    id: 1,
  });
  const encoded = JSON.stringify(response);
  assert.doesNotMatch(encoded, new RegExp(tailSentinel));
  assert.match(encoded, /\[REDACTED\]/);
});

test("redacts escaped assignment and underscore-delimited YAML secrets", async () => {
  const assignmentTail = "ASSIGNMENT_TAIL_LEAK";
  const yamlSentinel = "YAML_SECRET_SENTINEL";
  const response = await handleRpc({
    method:
      "missing-OPENAI_API_KEY=\"secret\\\"" +
      assignmentTail +
      "\" api_key: " +
      yamlSentinel,
    id: 1,
  });
  const encoded = JSON.stringify(response);
  assert.doesNotMatch(encoded, new RegExp(assignmentTail));
  assert.doesNotMatch(encoded, new RegExp(yamlSentinel));
  assert.match(encoded, /\[REDACTED\]/);
});

test("redacts multiline YAML secret blocks", async () => {
  const sentinel = "PLA605_PEM_BODY_LEAK";
  const response = await handleRpc({
    method:
      "missing-private_key: |\n  -----BEGIN PRIVATE KEY-----\n  " +
      sentinel +
      "\n  -----END PRIVATE KEY-----",
    id: 1,
  });
  assert.doesNotMatch(JSON.stringify(response), new RegExp(sentinel));
});

test("redacts multiline YAML secret blocks across blank lines", async () => {
  const sentinel = "PLA605_YAML_BLANK_LINE_LEAK";
  const response = await handleRpc({
    method:
      "missing-private_key: |\n  first paragraph\n\n  " +
      sentinel,
    id: 1,
  });
  assert.doesNotMatch(JSON.stringify(response), new RegExp(sentinel));
});

test("redacts multiline YAML secret blocks with indentation indicators", async () => {
  const sentinel = "PLA605_YAML_INDENT_INDICATOR_LEAK";
  const response = await handleRpc({
    method: "missing-private_key: |2\n  " + sentinel,
    id: 1,
  });
  assert.doesNotMatch(JSON.stringify(response), new RegExp(sentinel));
});

test("redacts quoted multiline YAML secret blocks", async () => {
  const sentinel = "PLA605_QUOTED_BLOCK_LEAK";
  const response = await handleRpc({
    method: "missing-'private_key': |\n  " + sentinel,
    id: 1,
  });
  assert.doesNotMatch(JSON.stringify(response), new RegExp(sentinel));
});

test("redacts quoted YAML scalar secret keys", async () => {
  const sentinel = "PLA605_QUOTED_YAML_SCALAR_LEAK";
  for (const quote of ["'", '"']) {
    const response = await handleRpc({
      method: "missing-" + quote + "api_key" + quote + ": " + quote + sentinel + quote,
      id: 1,
    });
    assert.doesNotMatch(JSON.stringify(response), new RegExp(sentinel));
  }
});

test("redacts nested YAML secret mapping parents", async () => {
  for (const key of [
    "'runtime_token'",
    "runtime_token",
    "'_runtime_token'",
    "'2fa_token'",
    "'runtime token'",
  ]) {
    const sentinel = "PLA605_NESTED_YAML_LEAK_" + key.replace(/[^A-Za-z0-9]/g, "_").toUpperCase();
    const response = await handleRpc({
      method: "missing-" + key + ":\n  value: " + sentinel,
      id: 1,
    });
    const encoded = JSON.stringify(response);
    assert.doesNotMatch(encoded, new RegExp(sentinel));
    assert.match(encoded, /\[REDACTED\]/);
  }
});

test("redacts flow YAML secret mapping parents", async () => {
  for (const [index, template] of [
    (sentinel) => "'runtime_token': { value: " + sentinel + " }",
    (sentinel) => "runtime_token: { value: " + sentinel + " }",
    (sentinel) => "'runtime_token': &anchor { value: " + sentinel + " }",
    (sentinel) => "'runtime_token': !!map &anchor { value: " + sentinel + " }",
    (sentinel) => "'runtime_token': [" + sentinel + "]",
  ].entries()) {
    const sentinel = "PLA605_FLOW_YAML_LEAK_" + index;
    const response = await handleRpc({
      method: "missing-" + template(sentinel),
      id: 1,
    });
    assert.doesNotMatch(JSON.stringify(response), new RegExp(sentinel));
  }
});

test("fails closed on incomplete flow YAML secret mappings", async () => {
  const sentinel = "PLA605_INCOMPLETE_FLOW_YAML_LEAK";
  const response = await handleRpc({
    method: "missing-'runtime_token': { value: " + sentinel,
    id: 1,
  });
  assert.doesNotMatch(JSON.stringify(response), new RegExp(sentinel));
  assert.equal(response.error.message, "[REDACTED]");
});

test("preserves safe fields in valid embedded JSON while redacting secrets", async () => {
  const sentinel = "PLA605_EMBEDDED_JSON_FLOW_GUARD";
  const response = await handleRpc({
    method:
      "missing-" +
      JSON.stringify({
        runtime_token: { value: sentinel },
        safe_status: "keep",
      }),
    id: 1,
  });
  const encoded = JSON.stringify(response);
  assert.doesNotMatch(encoded, new RegExp(sentinel));
  assert.match(response.error.message, /"safe_status":"keep"/);
});

test("redacts anchored nested YAML secret mappings", async () => {
  const sentinel = "PLA605_YAML_ANCHOR_LEAK";
  const response = await handleRpc({
    method: "missing-'runtime_token': &anchor\n  value: " + sentinel,
    id: 1,
  });
  assert.doesNotMatch(JSON.stringify(response), new RegExp(sentinel));
});

test("redacts tagged nested YAML secret mappings", async () => {
  const sentinel = "PLA605_YAML_TAG_LEAK";
  const response = await handleRpc({
    method: "missing-'runtime_token': !!map\n  value: " + sentinel,
    id: 1,
  });
  assert.doesNotMatch(JSON.stringify(response), new RegExp(sentinel));
});

test("redacts nested YAML secret mappings with tag and anchor metadata", async () => {
  for (const metadata of ["&anchor !!map", "!!map &anchor"]) {
    const sentinel = "PLA605_YAML_TAG_ANCHOR_LEAK";
    const response = await handleRpc({
      method: "missing-'runtime_token': " + metadata + "\n  value: " + sentinel,
      id: 1,
    });
    assert.doesNotMatch(JSON.stringify(response), new RegExp(sentinel), metadata);
  }
});

test("redacts quoted nested YAML secret mappings in native log tails", async () => {
  const sentinel = "PLA605_NATIVE_NESTED_YAML_LEAK";
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-nested-yaml-"));
  const bin = path.join(dir, "tunnel-client");
  const native = {
    launch_diagnostics: {
      log_tail: "'runtime_token':\n  _value: " + sentinel + " # note",
    },
    stderr: sentinel,
  };
  fs.writeFileSync(
    bin,
    [
      "#!/bin/sh",
      "printf '%s\n' '" + JSON.stringify(native).replace(/'/g, "'\\''") + "'",
      "exit 1",
      "",
    ].join("\n"),
    { mode: 0o700 },
  );

  try {
    const response = await handleRpc({
      method: "tools/call",
      id: 1,
      params: {
        name: "runtime_status",
        arguments: { alias: "demo", tunnel_client_bin: bin },
      },
    });
    const encoded = JSON.stringify(response);
    assert.doesNotMatch(encoded, new RegExp(sentinel));
    assert.ok(response.error.data && response.error.data.native, encoded);
    assert.equal(response.error.data.native.launch_diagnostics.log_tail, "[REDACTED]");
    assert.equal(response.error.data.native.stderr, "[REDACTED]");
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("redacts unquoted and flow YAML secrets from sibling diagnostics", async () => {
  for (const [label, logTail] of [
    ["unquoted", "runtime_token:\n  value: "],
    ["flow", "'runtime_token': { value: "],
    ["flow-sequence", "'runtime_token': { values: ["],
    ["nested-sequence", "'runtime_token': [["],
  ]) {
    const sentinel = "PLA605_NATIVE_" + label.toUpperCase() + "_YAML_LEAK";
    const suffix =
      label === "flow"
        ? " }"
        : label === "flow-sequence"
          ? "] }"
          : label === "nested-sequence"
            ? "]]"
            : "";
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-" + label + "-yaml-"));
    const bin = path.join(dir, "tunnel-client");
    const native = {
      launch_diagnostics: {
        log_tail: logTail + sentinel + suffix,
      },
      stderr: sentinel,
    };
    fs.writeFileSync(
      bin,
      [
        "#!/bin/sh",
        "printf '%s\n' '" + JSON.stringify(native).replace(/'/g, "'\\''") + "'",
        "exit 1",
        "",
      ].join("\n"),
      { mode: 0o700 },
    );

    try {
      const response = await handleRpc({
        method: "tools/call",
        id: 1,
        params: {
          name: "runtime_status",
          arguments: { alias: "demo", tunnel_client_bin: bin },
        },
      });
      const encoded = JSON.stringify(response);
      assert.doesNotMatch(encoded, new RegExp(sentinel), label);
      assert.equal(response.error.data.native.stderr, "[REDACTED]", label);
    } finally {
      fs.rmSync(dir, { recursive: true, force: true });
    }
  }
});

test("normalizes single-quoted YAML leaves for sibling redaction", async () => {
  const sentinel = "PLA605_SINGLE_QUOTE_LEAK'VALUE";
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-quoted-yaml-"));
  const bin = path.join(dir, "tunnel-client");
  const native = {
    launch_diagnostics: {
      log_tail: "'runtime_token': { value: 'PLA605_SINGLE_QUOTE_LEAK''VALUE' }",
    },
    stderr: sentinel,
  };
  fs.writeFileSync(
    bin,
    [
      "#!/bin/sh",
      "printf '%s\n' '" + JSON.stringify(native).replace(/'/g, "'\\''") + "'",
      "exit 1",
      "",
    ].join("\n"),
    { mode: 0o700 },
  );

  try {
    const response = await handleRpc({
      method: "tools/call",
      id: 1,
      params: {
        name: "runtime_status",
        arguments: { alias: "demo", tunnel_client_bin: bin },
      },
    });
    assert.doesNotMatch(JSON.stringify(response), /PLA605_SINGLE_QUOTE_LEAK/);
    assert.equal(response.error.data.native.stderr, "[REDACTED]");
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("normalizes double-quoted YAML escapes for sibling redaction", async () => {
  const sentinel = "PLA605_DOUBLE_ESCAPE_LEAK";
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-double-yaml-"));
  const bin = path.join(dir, "tunnel-client");
  const native = {
    launch_diagnostics: {
      log_tail: "'runtime_token': { value: \"PLA605\\x5fDOUBLE\\x5fESCAPE\\x5fLEAK\" }",
    },
    stderr: sentinel,
  };
  fs.writeFileSync(
    bin,
    [
      "#!/bin/sh",
      "printf '%s\n' '" + JSON.stringify(native).replace(/'/g, "'\\''") + "'",
      "exit 1",
      "",
    ].join("\n"),
    { mode: 0o700 },
  );

  try {
    const response = await handleRpc({
      method: "tools/call",
      id: 1,
      params: {
        name: "runtime_status",
        arguments: { alias: "demo", tunnel_client_bin: bin },
      },
    });
    assert.doesNotMatch(JSON.stringify(response), new RegExp(sentinel));
    assert.equal(response.error.data.native.stderr, "[REDACTED]");
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("redacts password aliases in diagnostics", async () => {
  const sentinel = "PLA605_PASSWORD_ALIAS_DIAGNOSTIC";
  for (const alias of ["pass", "passwd", "passphrase"]) {
    const response = await handleRpc({
      method: "missing-" + alias + ": " + sentinel,
      id: 1,
    });
    assert.doesNotMatch(JSON.stringify(response), new RegExp(sentinel), alias);
  }
});

test("redacts doubled single quotes inside YAML scalar secrets", async () => {
  const sentinel = "PLA605_YAML_DOUBLED_QUOTE_LEAK";
  const response = await handleRpc({
    method: "missing-'api_key': '" + sentinel + "''TAILLEAK'",
    id: 1,
  });
  const encoded = JSON.stringify(response);
  assert.doesNotMatch(encoded, new RegExp(sentinel));
  assert.doesNotMatch(encoded, /TAILLEAK/);
});

test("redacts standalone private-key PEM blocks", async () => {
  const sentinel = "PLA605_STANDALONE_PEM_LEAK";
  const pem = [
    "-----BEGIN PRIVATE KEY-----",
    sentinel,
    "-----END PRIVATE KEY-----",
  ].join("\n");
  const response = await handleRpc({ method: "missing-" + pem, id: 1 });
  assert.doesNotMatch(JSON.stringify(response), new RegExp(sentinel));
});

test("redacts standalone encrypted private-key PEM blocks", async () => {
  const sentinel = "PLA605_ENCRYPTED_PEM_LEAK";
  const pem = [
    "-----BEGIN ENCRYPTED PRIVATE KEY-----",
    sentinel,
    "-----END ENCRYPTED PRIVATE KEY-----",
  ].join("\n");
  const response = await handleRpc({ method: "missing-" + pem, id: 1 });
  assert.doesNotMatch(JSON.stringify(response), new RegExp(sentinel));
});

test("bounds malformed escaped diagnostics while redacting", async () => {
  const malformed = "\\".repeat(20000) + '\"';
  const response = await handleRpc({
    method: "missing-" + malformed,
    id: 1,
  });
  assert.equal(response.error.code, -32601);

  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-malformed-"));
  const bin = path.join(dir, "tunnel-client");
  fs.writeFileSync(
    bin,
    [
      "#!/bin/sh",
      "printf '%s\\n' '" + malformed + "' >&2",
      "exit 1",
      "",
    ].join("\n"),
    { mode: 0o700 },
  );
  try {
    const nativeResponse = await handleRpc({
      method: "tools/call",
      id: 2,
      params: {
        name: "runtime_status",
        arguments: { alias: "demo", tunnel_client_bin: bin },
      },
    });
    assert.equal(nativeResponse.error.code, -32000);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("bounds unmatched JSON nesting while redacting", async () => {
  const response = await handleRpc({
    method: "missing-" + "{".repeat(20000),
    id: 1,
  });
  assert.equal(response.error.message, "[REDACTED]");
});

test("redacts sensitive diagnostics through the bounded escaped JSON depth", async () => {
  const sentinel = "plain-text-four-times-escaped-secret";
  let escaped = JSON.stringify({ admin_key: sentinel });
  for (let pass = 0; pass < 4; pass += 1) {
    escaped = JSON.stringify(escaped);
  }
  const response = await handleRpc({
    method: "missing-" + escaped,
    id: 1,
  });
  assert.doesNotMatch(JSON.stringify(response), new RegExp(sentinel));
});

test("redacts payload-derived secrets from unlabeled native errors", async () => {
  const sentinel = "plain-text-shared-payload-secret";
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-shared-secret-"));
  const bin = path.join(dir, "tunnel-client");
  fs.writeFileSync(
    bin,
    [
      "#!/bin/sh",
      "printf '%s\\n' '{\"runtime_token\":\"" + sentinel + "\"}'",
      "printf '%s\\n' '" + sentinel + "' >&2",
      "exit 1",
      "",
    ].join("\n"),
    { mode: 0o700 },
  );

  try {
    const response = await handleRpc({
      method: "tools/call",
      id: 1,
      params: {
        name: "runtime_status",
        arguments: { alias: "demo", tunnel_client_bin: bin },
      },
    });
    const encoded = JSON.stringify(response);
    assert.equal(response.error.data.native.runtime_token, "[REDACTED]");
    assert.equal(response.error.message, "[REDACTED]");
    assert.doesNotMatch(encoded, new RegExp(sentinel));
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("collects secrets from unparsed native stdout before redacting stderr", async () => {
  const sentinel = "plain-text-unparsed-stdout-secret";
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-stdout-secret-"));
  const bin = path.join(dir, "tunnel-client");
  fs.writeFileSync(
    bin,
    [
      "#!/bin/sh",
      "printf '%s\\n' 'OPENAI_API_KEY=" + sentinel + "'",
      "printf '%s\\n' '" + sentinel + "' >&2",
      "exit 1",
      "",
    ].join("\n"),
    { mode: 0o700 },
  );

  try {
    const response = await handleRpc({
      method: "tools/call",
      id: 1,
      params: {
        name: "runtime_status",
        arguments: { alias: "demo", tunnel_client_bin: bin },
      },
    });
    const encoded = JSON.stringify(response);
    assert.equal(response.error.message, "[REDACTED]");
    assert.equal(response.error.data.stderr, "[REDACTED]");
    assert.doesNotMatch(encoded, new RegExp(sentinel));
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("collects multiline YAML secrets from stdout before redacting stderr", async () => {
  const sentinel = "PLA605_YAML_STDOUT_SECRET";
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-yaml-stdout-"));
  const bin = path.join(dir, "tunnel-client");
  fs.writeFileSync(
    bin,
    [
      "#!/bin/sh",
      "printf '%s\\n' 'private_key: |'",
      "printf '%s\\n' '  " + sentinel + "'",
      "printf '%s\\n' '" + sentinel + "' >&2",
      "exit 1",
      "",
    ].join("\n"),
    { mode: 0o700 },
  );

  try {
    const response = await handleRpc({
      method: "tools/call",
      id: 1,
      params: {
        name: "runtime_status",
        arguments: { alias: "demo", tunnel_client_bin: bin },
      },
    });
    const encoded = JSON.stringify(response);
    assert.doesNotMatch(encoded, new RegExp(sentinel));
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("bounds global secret substitution when native output has many values", async () => {
  const sentinel = "plain-text-overflow-secret";
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-secret-overflow-"));
  const bin = path.join(dir, "tunnel-client");
  const tokens = Array.from({ length: 300 }, (_, index) => "secret-" + index + "-value");
  fs.writeFileSync(
    bin,
    [
      "#!/bin/sh",
      "printf '%s\\n' '" + JSON.stringify({ runtime_token: tokens }).replace(/'/g, "'\\''") + "'",
      "printf '%s\\n' '" + sentinel + "' >&2",
      "exit 1",
      "",
    ].join("\n"),
    { mode: 0o700 },
  );

  try {
    const response = await handleRpc({
      method: "tools/call",
      id: 1,
      params: {
        name: "runtime_status",
        arguments: { alias: "demo", tunnel_client_bin: bin },
      },
    });
    assert.equal(response.error.message, "[REDACTED]");
    assert.doesNotMatch(JSON.stringify(response), new RegExp(sentinel));
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("preserves MCP content discriminators when successful redaction overflows", async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-success-overflow-"));
  const bin = path.join(dir, "tunnel-client");
  const tokens = Array.from({ length: 300 }, (_, index) => "secret-" + index + "-value");
  fs.writeFileSync(
    bin,
    [
      "#!/bin/sh",
      "printf '%s\n' '" + JSON.stringify({ runtime_token: tokens }).replace(/'/g, "'\''") + "'",
      "exit 0",
      "",
    ].join("\n"),
    { mode: 0o700 },
  );

  try {
    const response = await handleRpc({
      method: "tools/call",
      id: 1,
      params: {
        name: "runtime_status",
        arguments: { alias: "demo", tunnel_client_bin: bin },
      },
    });
    assert.equal(response.result.content[0].type, "text");
    assert.equal(response.result.content[0].text, "[REDACTED]");
    assert.equal(response.result.structuredContent, undefined);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("bounds redaction depth for deeply nested native JSON", async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-deep-json-"));
  const bin = path.join(dir, "tunnel-client");
  const deep = "[".repeat(12000) + '"leaf"' + "]".repeat(12000);
  fs.writeFileSync(
    bin,
    [
      "#!/bin/sh",
      "printf '%s\\n' '" + deep + "'",
      "exit 1",
      "",
    ].join("\n"),
    { mode: 0o700 },
  );

  try {
    const response = await handleRpc({
      method: "tools/call",
      id: 1,
      params: {
        name: "runtime_status",
        arguments: { alias: "demo", tunnel_client_bin: bin },
      },
    });
    assert.equal(response.error.code, -32000);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("redacts payload-derived values repeated in successful sibling fields", async () => {
  const sentinel = "PLA605_RESULT_DUPLICATE_SECRET_LEAK";
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-result-secret-"));
  const bin = path.join(dir, "tunnel-client");
  fs.writeFileSync(
    bin,
    [
      "#!/bin/sh",
      "printf '%s\\n' '" + JSON.stringify({ runtime_token: sentinel, echo: sentinel }).replace(/'/g, "'\\''") + "'",
      "exit 0",
      "",
    ].join("\n"),
    { mode: 0o700 },
  );

  try {
    const response = await handleRpc({
      method: "tools/call",
      id: 1,
      params: {
        name: "runtime_status",
        arguments: { alias: "demo", tunnel_client_bin: bin },
      },
    });
    const encoded = JSON.stringify(response);
    assert.doesNotMatch(encoded, new RegExp(sentinel));
    assert.equal(response.result.structuredContent.native.runtime_token, "[REDACTED]");
    assert.equal(response.result.structuredContent.native.echo, "[REDACTED]");
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("preserves short-value content and non-secret repair commands", async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-safe-status-"));
  const bin = path.join(dir, "tunnel-client");
  const repairCommand = "tunnel-client runtimes repair demo";
  fs.writeFileSync(
    bin,
    [
      "#!/bin/sh",
      "printf '%s\\n' '{\"alias\":\"xylophone\",\"runtime_token\":\"live\",\"runtime_state\":\"live\",\"launch_diagnostics\":{\"token\":\"x\"},\"repair_actions\":[{\"command\":\"" + repairCommand + "\",\"reason\":\"restart\"}]}'",
      "exit 0",
      "",
    ].join("\n"),
    { mode: 0o700 },
  );

  try {
    const response = await handleRpc({
      method: "tools/call",
      id: 1,
      params: {
        name: "runtime_status",
        arguments: { alias: "demo", tunnel_client_bin: bin },
      },
    });
    assert.equal(response.result.content[0].type, "text");
    assert.equal(response.result.structuredContent.alias, "xylophone");
    assert.equal(response.result.structuredContent.native.runtime_token, "[REDACTED]");
    assert.equal(response.result.structuredContent.native.runtime_state, "live");
    assert.equal(response.result.structuredContent.launch_diagnostics.token, "[REDACTED]");
    assert.equal(response.result.structuredContent.repair_actions[0].command, repairCommand);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("redacts status-shaped stop responses without dropping safe native fields", async () => {
  const sentinel = "PLA605_STOP_RUNTIME_SECRET";
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-stop-status-"));
  const bin = path.join(dir, "tunnel-client");
  const repairCommand = "tunnel-client runtimes connect --alias demo";
  const native = {
    alias: "demo",
    runtime_state: "stopped",
    healthy: false,
    ready: false,
    ui_url: "http://127.0.0.1:1234/ui",
    remote: { id: "tunnel_123" },
    process_running: false,
    process: { command: "node server.js --api-key " + sentinel },
    repair_actions: [
      {
        action: "restart",
        reason: "runtime stopped",
        command: repairCommand,
      },
    ],
  };
  fs.writeFileSync(
    bin,
    [
      "#!/bin/sh",
      "printf '%s\\n' '" + JSON.stringify(native).replace(/'/g, "'\\''") + "'",
      "exit 0",
      "",
    ].join("\n"),
    { mode: 0o700 },
  );

  try {
    const response = await handleRpc({
      method: "tools/call",
      id: 1,
      params: {
        name: "stop_runtime",
        arguments: { alias: "demo", tunnel_client_bin: bin },
      },
    });
    const payload = response.result.structuredContent;
    assert.doesNotMatch(JSON.stringify(response), new RegExp(sentinel));
    assert.equal(payload.native.runtime_state, native.runtime_state);
    assert.equal(payload.native.healthy, native.healthy);
    assert.equal(payload.native.ready, native.ready);
    assert.equal(payload.native.ui_url, native.ui_url);
    assert.deepEqual(payload.native.remote, native.remote);
    assert.equal(payload.native.process_running, native.process_running);
    assert.equal(payload.live_process_command, "node server.js --api-key [REDACTED]");
    assert.equal(payload.repair_actions[0].reason, "runtime stopped");
    assert.equal(payload.repair_actions[0].command, repairCommand);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("preserves non-secret child-process failure diagnostics", async () => {
  const diagnostic = "unknown flag: --legacy-compatible";
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-safe-error-"));
  const bin = path.join(dir, "tunnel-client");
  fs.writeFileSync(
    bin,
    [
      "#!/bin/sh",
      "printf '%s\\n' '" + diagnostic + "' >&2",
      "exit 2",
      "",
    ].join("\n"),
    { mode: 0o700 },
  );

  try {
    const response = await handleRpc({
      method: "tools/call",
      id: 1,
      params: {
        name: "runtime_status",
        arguments: { alias: "demo", tunnel_client_bin: bin },
      },
    });
    assert.equal(response.error.message, diagnostic);
    assert.equal(response.error.data.stderr, diagnostic);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("redacts equals-form mcp command arguments in native command arrays", async () => {
  const sentinel = "PLA605_COMPOUND_MCP_LEAK";
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-command-equals-"));
  const bin = path.join(dir, "tunnel-client");
  fs.writeFileSync(
    bin,
    [
      "#!/bin/sh",
      "printf '%s\\n' '" + JSON.stringify({ command: ["tunnel-client", "runtimes", "connect", "--mcp-command=echo " + sentinel] }).replace(/'/g, "'\\''") + "'",
      "exit 1",
      "",
    ].join("\n"),
    { mode: 0o700 },
  );

  try {
    const response = await handleRpc({
      method: "tools/call",
      id: 1,
      params: {
        name: "runtime_status",
        arguments: { alias: "demo", tunnel_client_bin: bin },
      },
    });
    assert.doesNotMatch(JSON.stringify(response), new RegExp(sentinel));
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("redacts string-form mcp command fields", async () => {
  const structuredSentinel = "PLA605_STRUCTURED_MCP_COMMAND_LEAK";
  const commandSentinel = "PLA605_STRING_COMMAND_LEAK";
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-command-string-"));
  const bin = path.join(dir, "tunnel-client");
  const native = {
    mcp_command: "echo " + structuredSentinel,
    command:
      "tunnel-client runtimes connect --mcp-command=server --port " +
      commandSentinel +
      " --json",
  };
  fs.writeFileSync(
    bin,
    [
      "#!/bin/sh",
      "printf '%s\\n' '" + JSON.stringify(native).replace(/'/g, "'\\''") + "'",
      "exit 1",
      "",
    ].join("\n"),
    { mode: 0o700 },
  );

  try {
    const response = await handleRpc({
      method: "tools/call",
      id: 1,
      params: {
        name: "runtime_status",
        arguments: { alias: "demo", tunnel_client_bin: bin },
      },
    });
    const encoded = JSON.stringify(response);
    assert.doesNotMatch(encoded, new RegExp(structuredSentinel));
    assert.doesNotMatch(encoded, new RegExp(commandSentinel));
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("collects masked command fields before redacting sibling diagnostics", async () => {
  const sentinel = "PLA605_ECHOED_COMMAND_SECRET";
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-command-echo-"));
  const bin = path.join(dir, "tunnel-client");
  const command = "tunnel-client runtimes connect --mcp-command=echo " + sentinel;
  fs.writeFileSync(
    bin,
    [
      "#!/bin/sh",
      "printf '%s\n' '" + JSON.stringify({ command }).replace(/'/g, "'\''") + "'",
      "printf '%s\n' '" + command + "' >&2",
      "exit 1",
      "",
    ].join("\n"),
    { mode: 0o700 },
  );

  try {
    const response = await handleRpc({
      method: "tools/call",
      id: 1,
      params: {
        name: "runtime_status",
        arguments: { alias: "demo", tunnel_client_bin: bin },
      },
    });
    assert.doesNotMatch(JSON.stringify(response), new RegExp(sentinel));
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("preserves structured path and env locator fields", async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-locator-fields-"));
  const bin = path.join(dir, "tunnel-client");
  const native = {
    token_file: "/run/secrets/token",
    api_key_env: "OPENAI_API_KEY",
    credentials_path: "/home/me/.aws/credentials",
    authorization_url: "https://login.example.test/oauth",
    password_min_length: 12,
    cookie_domain: "example.test",
  };
  fs.writeFileSync(
    bin,
    [
      "#!/bin/sh",
      "printf '%s\\n' '" + JSON.stringify(native).replace(/'/g, "'\\''") + "'",
      "exit 0",
      "",
    ].join("\n"),
    { mode: 0o700 },
  );

  try {
    const response = await handleRpc({
      method: "tools/call",
      id: 1,
      params: {
        name: "runtime_status",
        arguments: { alias: "demo", tunnel_client_bin: bin },
      },
    });
    assert.equal(response.result.structuredContent.native.token_file, native.token_file);
    assert.equal(response.result.structuredContent.native.api_key_env, native.api_key_env);
    assert.equal(response.result.structuredContent.native.credentials_path, native.credentials_path);
    assert.equal(response.result.structuredContent.native.authorization_url, native.authorization_url);
    assert.equal(response.result.structuredContent.native.password_min_length, native.password_min_length);
    assert.equal(response.result.structuredContent.native.cookie_domain, native.cookie_domain);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("does not promote safe locator paths into global substitutions", async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-locator-echo-"));
  const bin = path.join(dir, "tunnel-client");
  const locator = "/run/secrets/token";
  const repairCommand = "cat " + locator;
  const native = {
    token_file: locator,
    environment: [{ name: "TOKEN_FILE", value: locator }],
    profile_path: locator,
    repair_actions: [{ command: repairCommand }],
  };
  fs.writeFileSync(
    bin,
    [
      "#!/bin/sh",
      "printf '%s\n' '" + JSON.stringify(native).replace(/'/g, "'\''") + "'",
      "exit 0",
      "",
    ].join("\n"),
    { mode: 0o700 },
  );

  try {
    const response = await handleRpc({
      method: "tools/call",
      id: 1,
      params: {
        name: "runtime_status",
        arguments: { alias: "demo", tunnel_client_bin: bin },
      },
    });
    assert.equal(response.result.structuredContent.native.token_file, locator);
    assert.equal(response.result.structuredContent.native.environment[0].value, locator);
    assert.equal(response.result.structuredContent.profile_path, locator);
    assert.equal(response.result.structuredContent.repair_actions[0].command, repairCommand);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("preserves safe locator values in textual diagnostics", async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-text-locator-"));
  const bin = path.join(dir, "tunnel-client");
  const locator = "/run/secrets/token";
  const repairCommand = "cat " + locator;
  const native = {
    profile_path: locator,
    stderr: "token_file: " + locator,
    repair_actions: [{ command: repairCommand }],
  };
  fs.writeFileSync(
    bin,
    [
      "#!/bin/sh",
      "printf '%s\n' '" + JSON.stringify(native).replace(/'/g, "'\''") + "'",
      "exit 0",
      "",
    ].join("\n"),
    { mode: 0o700 },
  );

  try {
    const response = await handleRpc({
      method: "tools/call",
      id: 1,
      params: {
        name: "runtime_status",
        arguments: { alias: "demo", tunnel_client_bin: bin },
      },
    });
    assert.equal(response.result.structuredContent.profile_path, locator);
    assert.equal(response.result.structuredContent.native.stderr, native.stderr);
    assert.equal(response.result.structuredContent.repair_actions[0].command, repairCommand);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("preserves non-secret YAML block configuration", async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-yaml-config-"));
  const bin = path.join(dir, "tunnel-client");
  const url = "https://login.example.test/oauth";
  const repairCommand = "open " + url;
  const native = {
    ui_url: url,
    stderr: [
      "authorization_url: |",
      "  " + url,
      "api_key_enabled: |",
      "  true",
      "api_key_header: |",
      "  Authorization",
      "password_hash_algorithm: |",
      "  argon2",
      "",
    ].join("\n"),
    repair_actions: [{ command: repairCommand }],
  };
  fs.writeFileSync(
    bin,
    [
      "#!/bin/sh",
      "printf '%s\n' '" + JSON.stringify(native).replace(/'/g, "'\''") + "'",
      "exit 0",
      "",
    ].join("\n"),
    { mode: 0o700 },
  );

  try {
    const response = await handleRpc({
      method: "tools/call",
      id: 1,
      params: {
        name: "runtime_status",
        arguments: { alias: "demo", tunnel_client_bin: bin },
      },
    });
    assert.equal(response.result.structuredContent.native.ui_url, url);
    assert.equal(response.result.structuredContent.native.stderr, native.stderr);
    assert.equal(response.result.structuredContent.repair_actions[0].command, repairCommand);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("redacts sensitive entry tuples under header and environment containers", async () => {
  const headerSentinel = "PLA605_TUPLE_HEADER_SECRET";
  const envSentinel = "PLA605_TUPLE_ENV_SECRET";
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-entry-tuples-"));
  const bin = path.join(dir, "tunnel-client");
  const native = {
    headers: [["Authorization", headerSentinel]],
    environment: [["OPENAI_API_KEY", envSentinel]],
    header_echo: headerSentinel,
    env_echo: envSentinel,
  };
  fs.writeFileSync(
    bin,
    [
      "#!/bin/sh",
      "printf '%s\n' '" + JSON.stringify(native).replace(/'/g, "'\''") + "'",
      "exit 0",
      "",
    ].join("\n"),
    { mode: 0o700 },
  );

  try {
    const response = await handleRpc({
      method: "tools/call",
      id: 1,
      params: {
        name: "runtime_status",
        arguments: { alias: "demo", tunnel_client_bin: bin },
      },
    });
    const payload = response.result.structuredContent;
    assert.doesNotMatch(JSON.stringify(response), new RegExp(headerSentinel + "|" + envSentinel));
    assert.equal(payload.native.headers[0][0], "Authorization");
    assert.equal(payload.native.headers[0][1], "[REDACTED]");
    assert.equal(payload.native.environment[0][0], "OPENAI_API_KEY");
    assert.equal(payload.native.environment[0][1], "[REDACTED]");
    assert.equal(payload.native.header_echo, "[REDACTED]");
    assert.equal(payload.native.env_echo, "[REDACTED]");
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("does not treat ordinary data arrays as command arguments", async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-data-array-"));
  const bin = path.join(dir, "tunnel-client");
  const native = { labels: ["token", "production"], alias: "production" };
  fs.writeFileSync(
    bin,
    [
      "#!/bin/sh",
      "printf '%s\n' '" + JSON.stringify(native).replace(/'/g, "'\''") + "'",
      "exit 0",
      "",
    ].join("\n"),
    { mode: 0o700 },
  );

  try {
    const response = await handleRpc({
      method: "tools/call",
      id: 1,
      params: {
        name: "runtime_status",
        arguments: { alias: "demo", tunnel_client_bin: bin },
      },
    });
    assert.deepEqual(response.result.structuredContent.native.labels, native.labels);
    assert.equal(response.result.structuredContent.alias, "production");
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("preserves ordinary property names containing key substrings", async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-property-names-"));
  const bin = path.join(dir, "tunnel-client");
  const native = {
    keyboard_layout: "us",
    monkey: "ok",
    keyspace: "wide",
    OPENAI_API_KEY_ENABLED: true,
    API_KEY_HEADER: "Authorization",
    PASSWORD_HASH_ALGORITHM: "argon2",
  };
  fs.writeFileSync(
    bin,
    [
      "#!/bin/sh",
      "printf '%s\n' '" + JSON.stringify(native).replace(/'/g, "'\''") + "'",
      "exit 0",
      "",
    ].join("\n"),
    { mode: 0o700 },
  );
  try {
    const response = await handleRpc({
      method: "tools/call",
      id: 1,
      params: { name: "runtime_status", arguments: { alias: "demo", tunnel_client_bin: bin } },
    });
    assert.equal(response.result.structuredContent.native.keyboard_layout, "us");
    assert.equal(response.result.structuredContent.native.monkey, "ok");
    assert.equal(response.result.structuredContent.native.keyspace, "wide");
    assert.equal(response.result.structuredContent.native.OPENAI_API_KEY_ENABLED, true);
    assert.equal(response.result.structuredContent.native.API_KEY_HEADER, "Authorization");
    assert.equal(response.result.structuredContent.native.PASSWORD_HASH_ALGORITHM, "argon2");
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("redacts secret-shaped object property names", async () => {
  const sentinel = "sk-proj-abcdefghijklmnop";
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-secret-key-name-"));
  const bin = path.join(dir, "tunnel-client");
  const native = { token_map: { [sentinel]: true } };
  fs.writeFileSync(
    bin,
    [
      "#!/bin/sh",
      "printf '%s\n' '" + JSON.stringify(native).replace(/'/g, "'\''") + "'",
      "exit 0",
      "",
    ].join("\n"),
    { mode: 0o700 },
  );

  try {
    const response = await handleRpc({
      method: "tools/call",
      id: 1,
      params: {
        name: "runtime_status",
        arguments: { alias: "demo", tunnel_client_bin: bin },
      },
    });
    assert.doesNotMatch(JSON.stringify(response), new RegExp(sentinel));
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("redacts credential keys inside sensitive subtrees and sibling echoes", async () => {
  const sentinel = "PLA605_KEY_SECRET";
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-sensitive-key-name-"));
  const bin = path.join(dir, "tunnel-client");
  const native = { token: { [sentinel]: true }, echo: sentinel };
  fs.writeFileSync(
    bin,
    [
      "#!/bin/sh",
      "printf '%s\n' '" + JSON.stringify(native).replace(/'/g, "'\''") + "'",
      "exit 0",
      "",
    ].join("\n"),
    { mode: 0o700 },
  );

  try {
    const response = await handleRpc({
      method: "tools/call",
      id: 1,
      params: {
        name: "runtime_status",
        arguments: { alias: "demo", tunnel_client_bin: bin },
      },
    });
    assert.doesNotMatch(JSON.stringify(response), new RegExp(sentinel));
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("redacts values selected by sensitive sibling names", async () => {
  const sentinel = "PLA605_NAME_VALUE_SECRET";
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-name-value-"));
  const bin = path.join(dir, "tunnel-client");
  const native = { environment: [{ name: "OPENAI_API_KEY", value: sentinel }] };
  fs.writeFileSync(
    bin,
    [
      "#!/bin/sh",
      "printf '%s\n' '" + JSON.stringify(native).replace(/'/g, "'\''") + "'",
      "exit 0",
      "",
    ].join("\n"),
    { mode: 0o700 },
  );

  try {
    const response = await handleRpc({
      method: "tools/call",
      id: 1,
      params: {
        name: "runtime_status",
        arguments: { alias: "demo", tunnel_client_bin: bin },
      },
    });
    assert.doesNotMatch(JSON.stringify(response), new RegExp(sentinel));
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("still redacts adjacent sensitive values in command arrays", async () => {
  const sentinel = "PLA605_COMMAND_ARRAY_SECRET";
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "tunnel-mcp-command-array-"));
  const bin = path.join(dir, "tunnel-client");
  const native = { command: ["node", "--token", sentinel] };
  fs.writeFileSync(
    bin,
    [
      "#!/bin/sh",
      "printf '%s\n' '" + JSON.stringify(native).replace(/'/g, "'\''") + "'",
      "exit 1",
      "",
    ].join("\n"),
    { mode: 0o700 },
  );

  try {
    const response = await handleRpc({
      method: "tools/call",
      id: 1,
      params: {
        name: "runtime_status",
        arguments: { alias: "demo", tunnel_client_bin: bin },
      },
    });
    assert.doesNotMatch(JSON.stringify(response), new RegExp(sentinel));
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("preserves JSON-RPC ids while redacting response content", async () => {
  const id = "sk-test-id-sentinel";
  const response = await handleRpc({ method: "initialize", id });
  assert.equal(response.id, id);
});

test("preserves tool input schemas in tools/list", async () => {
  const response = await handleRpc({ method: "tools/list", id: 1 });
  const connect = response.result.tools.find((tool) => tool.name === "connect_stdio_mcp");
  assert.equal(connect.inputSchema.properties.mcp_command.type, "string");
  assert.equal(connect.inputSchema.properties.runtime_api_key.type, "string");
});
