// Agent Monitor — OpenCode Plugin
import { spawnSync } from "child_process";
import { readFileSync } from "fs";
import { join } from "path";
import { homedir } from "os";

export const AgentMonitorPlugin = async ({ client, directory }) => {
  const HOOK_BIN = "__HOOK_BIN_PATH__";
  const msgRoles = new Map();
  const startedSessions = new Set();

  function extractSid(props) {
    // sessionID is the canonical session identifier across all OpenCode events.
    // Do NOT use info.id — that is the message ID for message.updated events.
    return props?.sessionID || props?.info?.sessionID ||
           props?.part?.sessionID || "";
  }

  function sendNative(type, sessionId, payload) {
    try {
      spawnSync(HOOK_BIN,
        ["--agent-type", "opencode", "--event", type, "--session-id", sessionId || ""],
        { input: JSON.stringify(payload), timeout: 5000 }
      );
    } catch {}
  }

  function readToken() {
    try {
      return readFileSync(join(homedir(), ".agent-monitor", "local-token"), "utf8").trim();
    } catch { return ""; }
  }

  // ── Poll daemon for web inputs ──
  let polling = false;
  async function pollInputs() {
    if (polling) return;
    const token = readToken();
    if (!token) return;
    polling = true;
    try {
      for (const sid of startedSessions) {
        const url = "http://127.0.0.1:9101/api/poll-input?agent_type=opencode&agent_session_id=" + encodeURIComponent(sid);
        const ctrl = new AbortController();
        const t = setTimeout(() => ctrl.abort(), 3000);
        let res;
        try {
          res = await fetch(url, { headers: { "X-Daemon-Token": token }, signal: ctrl.signal });
        } catch { clearTimeout(t); continue; }
        clearTimeout(t);
        if (res.status === 200) {
          const data = await res.json();
          if (data.text) {
            client.session.prompt({
              path: { id: sid },
              body: { parts: [{ type: "text", text: data.text }] }
            });
          }
        }
      }
    } catch {}
    polling = false;
  }
  setInterval(pollInputs, 1000);
  pollInputs();

  return {
    "event": async ({ event: ev }) => {
      const t = ev.type;
      const p = ev.properties || {};
      const sid = extractSid(p);

      // Track session lifecycle
      if (t === "session.created") {
        const id = p?.info?.id;
        if (id) startedSessions.add(id);
      }

      // Track message roles for .updated correlation
      if (t === "message.updated") {
        const info = p?.info;
        if (info?.id && info?.sessionID) {
          msgRoles.set(info.id, { role: info.role, sessionID: info.sessionID });
        }
      }

      // Build payload — for message.part.updated text parts, embed the
      // message role so the session manager can distinguish user vs assistant.
      // The OpenCode event name is "message.part.updated"; the previous check
      // `t === ".updated"` never matched, so _role was never attached and the
      // handler could not classify user prompts (titles stayed empty).
      let payload = p;
      if (t === "message.part.updated") {
        const part = p?.part;
        if (part?.type === "text" && part?.messageID) {
          const meta = msgRoles.get(part.messageID);
          if (meta) {
            payload = { ...p, _role: meta.role };
          }
        }
      }

      sendNative(t, sid, payload);
    },
  };
};
