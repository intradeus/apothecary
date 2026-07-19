#!/usr/bin/env node
// ask_human MCP stdio server (DESIGN.md §10).
//
// Exposes one tool: ask_human{type, question, context?}. Protocol with the
// engine: POST the ping (per-run bearer token); on 204 re-poll every 5 s
// (the server holds 55 s) indefinitely — the stage timeout is suspended
// while a ping is pending. On network error retry with backoff capped at
// 60 s; if the engine is unreachable > 10 min, return a tool error string
// and let the agent proceed. Credentials come from /factory/run.env and are
// re-read on 401 (the engine regenerates tokens on restart).

import { readFileSync } from "node:fs";
import { createInterface } from "node:readline";

const RUN_ENV = "/factory/run.env";
const POLL_INTERVAL_MS = 5_000;
const UNREACHABLE_LIMIT_MS = 10 * 60_000;

function runEnv() {
  const env = {};
  for (const line of readFileSync(RUN_ENV, "utf8").split("\n")) {
    const i = line.indexOf("=");
    if (i > 0) env[line.slice(0, i)] = line.slice(i + 1);
  }
  return env;
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function callEngine(method, path, body, env) {
  const res = await fetch(env.FACTORY_ENGINE_URL + path, {
    method,
    headers: {
      Authorization: `Bearer ${env.FACTORY_RUN_TOKEN}`,
      "Content-Type": "application/json",
    },
    body: body ? JSON.stringify(body) : undefined,
  });
  return res;
}

// askHuman drives the full §10 protocol; returns the answer text or throws.
async function askHuman(args) {
  let env = runEnv();
  let pingId = null;
  let downSince = null;
  let backoff = 1_000;

  for (;;) {
    let res;
    try {
      if (pingId === null) {
        res = await callEngine("POST", `/api/runs/${env.FACTORY_RUN_ID}/pings`, {
          type: args.type,
          question: args.question,
          context: args.context ?? "",
        }, env);
      } else {
        res = await callEngine("GET", `/api/runs/${env.FACTORY_RUN_ID}/pings/${pingId}`, null, env);
      }
    } catch (err) {
      downSince ??= Date.now();
      if (Date.now() - downSince > UNREACHABLE_LIMIT_MS) {
        throw new Error(`ask_human unavailable: ${err.message}`);
      }
      await sleep(backoff);
      backoff = Math.min(backoff * 2, 60_000);
      continue;
    }
    downSince = null;
    backoff = 1_000;

    if (res.status === 401) {
      // Engine restarted and rotated the run token; pick up the fresh one.
      await sleep(POLL_INTERVAL_MS);
      env = runEnv();
      continue;
    }
    if (res.status === 200) {
      const body = await res.json();
      return body.feedback ? `${body.answer}\n${body.feedback}` : body.answer;
    }
    if (res.status === 204) {
      pingId = res.headers.get("x-ping-id") ?? pingId;
      await sleep(POLL_INTERVAL_MS);
      continue;
    }
    const text = await res.text();
    throw new Error(`ask_human failed: engine returned ${res.status}: ${text.slice(0, 300)}`);
  }
}

// --- minimal MCP over stdio (JSON-RPC 2.0, one message per line) ------------

const TOOL = {
  name: "ask_human",
  description:
    "Ask the human operator a question and wait for their answer. Use when " +
    "blocked: unclear requirements (type=clarification), an obstacle you " +
    "cannot resolve (type=blocker), or an action needing approval " +
    "(type=permission). The answer is returned as text.",
  inputSchema: {
    type: "object",
    properties: {
      type: { type: "string", enum: ["clarification", "blocker", "permission"] },
      question: { type: "string", description: "The question for the human" },
      context: { type: "string", description: "Optional supporting context" },
    },
    required: ["type", "question"],
  },
};

const send = (msg) => process.stdout.write(JSON.stringify(msg) + "\n");
const reply = (id, result) => send({ jsonrpc: "2.0", id, result });
const replyErr = (id, code, message) => send({ jsonrpc: "2.0", id, error: { code, message } });

const rl = createInterface({ input: process.stdin });
rl.on("line", async (line) => {
  line = line.trim();
  if (!line) return;
  let msg;
  try {
    msg = JSON.parse(line);
  } catch {
    return;
  }
  const { id, method, params } = msg;
  if (id === undefined) return; // notification
  switch (method) {
    case "initialize":
      reply(id, {
        protocolVersion: params?.protocolVersion ?? "2024-11-05",
        capabilities: { tools: {} },
        serverInfo: { name: "factory-ask-human", version: "1.0.0" },
      });
      break;
    case "tools/list":
      reply(id, { tools: [TOOL] });
      break;
    case "tools/call": {
      if (params?.name !== "ask_human") {
        replyErr(id, -32602, `unknown tool ${params?.name}`);
        break;
      }
      try {
        const answer = await askHuman(params.arguments ?? {});
        reply(id, { content: [{ type: "text", text: answer }] });
      } catch (err) {
        // Tool-level error string (§10), not a protocol error: the agent
        // sees it and proceeds.
        reply(id, { content: [{ type: "text", text: String(err.message) }], isError: true });
      }
      break;
    }
    case "ping":
      reply(id, {});
      break;
    default:
      replyErr(id, -32601, `method ${method} not found`);
  }
});
