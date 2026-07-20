import { d as attr, b as ensure_array_like, e as escape_html, c as attr_style } from "../../../chunks/index.js";
import { b as api } from "../../../chunks/api.js";
function IntegrationForm($$renderer, $$props) {
  $$renderer.component(($$renderer2) => {
    let { descriptor, existing = null, onsaved, oncancel } = $$props;
    const fields = descriptor.fields ?? [];
    let name = existing?.name ?? descriptor.label;
    let values = Object.fromEntries(fields.filter((f) => !f.secret).map((f) => [
      f.name,
      String(existing?.config?.[f.name] ?? f.default ?? "")
    ]));
    let secretValues = Object.fromEntries(fields.filter((f) => f.secret).map((f) => [f.name, ""]));
    let busy = false;
    const isMultiline = (n) => n.endsWith("_json");
    $$renderer2.push(`<form><label for="if-name">Name</label> <input id="if-name"${attr("value", name)} required=""/> <!--[-->`);
    const each_array = ensure_array_like(fields);
    for (let $$index = 0, $$length = each_array.length; $$index < $$length; $$index++) {
      let f = each_array[$$index];
      $$renderer2.push(`<label${attr("for", `if-${f.name}`)}>${escape_html(f.label)}`);
      if (f.secret) {
        $$renderer2.push("<!--[0-->");
        $$renderer2.push(`<span class="muted">— ${escape_html(existing?.secrets?.[f.name] ? `set (${existing.secrets[f.name]}); leave empty to keep` : "write-only")}</span>`);
      } else {
        $$renderer2.push("<!--[-1-->");
      }
      $$renderer2.push(`<!--]--></label> `);
      if (f.secret && isMultiline(f.name)) {
        $$renderer2.push("<!--[0-->");
        $$renderer2.push(`<textarea${attr("id", `if-${f.name}`)}${attr("placeholder", existing?.secrets?.[f.name] ? "(unchanged)" : f.placeholder ?? "")} rows="4" class="mono">`);
        const $$body = escape_html(secretValues[f.name]);
        if ($$body) {
          $$renderer2.push(`${$$body}`);
        }
        $$renderer2.push(`</textarea>`);
      } else if (f.secret) {
        $$renderer2.push("<!--[1-->");
        $$renderer2.push(`<input${attr("id", `if-${f.name}`)} type="password"${attr("value", secretValues[f.name])}${attr("placeholder", existing?.secrets?.[f.name] ? "(unchanged)" : f.placeholder ?? "")}${attr("required", f.required && !existing?.secrets?.[f.name], true)} autocomplete="off"/>`);
      } else {
        $$renderer2.push("<!--[-1-->");
        $$renderer2.push(`<input${attr("id", `if-${f.name}`)}${attr("value", values[f.name])}${attr("placeholder", f.placeholder ?? f.default ?? "")}${attr("required", f.required && !f.default, true)}/>`);
      }
      $$renderer2.push(`<!--]-->`);
    }
    $$renderer2.push(`<!--]--> `);
    {
      $$renderer2.push("<!--[-1-->");
    }
    $$renderer2.push(`<!--]--> <div class="row" style="margin-top:14px"><button class="primary" type="submit"${attr("disabled", busy, true)}>${escape_html(existing ? "Save changes" : `Add ${descriptor.label}`)}</button> `);
    if (oncancel) {
      $$renderer2.push("<!--[0-->");
      $$renderer2.push(`<button type="button">Cancel</button>`);
    } else {
      $$renderer2.push("<!--[-1-->");
    }
    $$renderer2.push(`<!--]--></div></form>`);
  });
}
function _page($$renderer, $$props) {
  $$renderer.component(($$renderer2) => {
    const portOrder = ["forge", "provisioner", "harness"];
    const portTitles = {
      forge: "Forge (code hosting)",
      provisioner: "Provisioner (workspaces)",
      harness: "Harness (coding agent & model auth)"
    };
    let descriptors = [];
    let integrations = [];
    let loaded = false;
    let error = "";
    let adding = {};
    let editing = null;
    let testResults = {};
    const refresh = async () => {
      try {
        [descriptors, integrations] = await Promise.all([api.kinds(), api.integrations()]);
        loaded = true;
        error = "";
      } catch (e) {
        error = e instanceof Error ? e.message : String(e);
      }
    };
    const kindsFor = (port) => descriptors.filter((d) => d.port === port);
    const rowsFor = (port) => integrations.filter((i) => i.port === port);
    const descriptorOf = (i) => descriptors.find((d) => d.port === i.port && d.kind === i.kind);
    function configSummary(i) {
      const parts = Object.entries(i.config).map(([k, v]) => `${k}=${v}`);
      for (const k of Object.keys(i.secrets)) parts.push(`${k}=${i.secrets[k]}`);
      return parts.join(" · ");
    }
    $$renderer2.push(`<h1>Integrations</h1> <p class="muted small" style="margin-top:-8px">Credentialed connections the factory works with. Secrets are write-only: they are stored
  encrypted and this page only ever shows a mask.</p> `);
    if (error) {
      $$renderer2.push("<!--[0-->");
      $$renderer2.push(`<div class="error">${escape_html(error)}</div>`);
    } else {
      $$renderer2.push("<!--[-1-->");
    }
    $$renderer2.push(`<!--]--> <!--[-->`);
    const each_array = ensure_array_like(portOrder);
    for (let $$index_2 = 0, $$length = each_array.length; $$index_2 < $$length; $$index_2++) {
      let port = each_array[$$index_2];
      $$renderer2.push(`<h2 style="margin-top:24px">${escape_html(portTitles[port])}</h2> `);
      const each_array_1 = ensure_array_like(rowsFor(port));
      if (each_array_1.length !== 0) {
        $$renderer2.push("<!--[-->");
        for (let $$index = 0, $$length2 = each_array_1.length; $$index < $$length2; $$index++) {
          let i = each_array_1[$$index];
          $$renderer2.push(`<div class="card"><div class="row spread"><div><strong>${escape_html(i.name)}</strong> <span class="chip">${escape_html(i.kind)}</span> <div class="mono small muted">${escape_html(configSummary(i))}</div></div> <div class="row">`);
          if (testResults[i.id] === "running") {
            $$renderer2.push("<!--[0-->");
            $$renderer2.push(`<span class="muted small">testing…</span>`);
          } else if (testResults[i.id]) {
            $$renderer2.push("<!--[1-->");
            const r = testResults[i.id];
            $$renderer2.push(`<span class="small"${attr_style(`color:${r.ok ? "var(--green)" : "var(--red)"}`)}>${escape_html(r.ok ? "✓ ok" : `✗ ${r.error}`)}</span>`);
          } else {
            $$renderer2.push("<!--[-1-->");
          }
          $$renderer2.push(`<!--]--> <button>Test</button> <button>${escape_html(editing === i.id ? "Close" : "Edit")}</button> <button class="danger">Delete</button></div></div> `);
          if (editing === i.id) {
            $$renderer2.push("<!--[0-->");
            const d = descriptorOf(i);
            if (d) {
              $$renderer2.push("<!--[0-->");
              $$renderer2.push(`<div style="margin-top:12px; border-top:1px solid var(--border); padding-top:4px">`);
              IntegrationForm($$renderer2, {
                descriptor: d,
                existing: i,
                onsaved: () => {
                  editing = null;
                  refresh();
                },
                oncancel: () => editing = null
              });
              $$renderer2.push(`<!----></div>`);
            } else {
              $$renderer2.push("<!--[-1-->");
              $$renderer2.push(`<div class="muted small">Kind "${escape_html(i.kind)}" is not registered on this server.</div>`);
            }
            $$renderer2.push(`<!--]-->`);
          } else {
            $$renderer2.push("<!--[-1-->");
          }
          $$renderer2.push(`<!--]--></div>`);
        }
      } else {
        $$renderer2.push("<!--[!-->");
        $$renderer2.push(`<div class="muted small" style="margin:6px 0 10px">None configured.</div>`);
      }
      $$renderer2.push(`<!--]--> `);
      if (adding[port]) {
        $$renderer2.push("<!--[0-->");
        const d = kindsFor(port).find((k) => k.kind === adding[port]);
        if (d) {
          $$renderer2.push("<!--[0-->");
          $$renderer2.push(`<div class="card"><h2>Add ${escape_html(d.label)}</h2> `);
          IntegrationForm($$renderer2, {
            descriptor: d,
            onsaved: () => {
              adding = { ...adding, [port]: void 0 };
              refresh();
            },
            oncancel: () => adding = { ...adding, [port]: void 0 }
          });
          $$renderer2.push(`<!----></div>`);
        } else {
          $$renderer2.push("<!--[-1-->");
        }
        $$renderer2.push(`<!--]-->`);
      } else {
        $$renderer2.push("<!--[-1-->");
        $$renderer2.push(`<div class="row"><!--[-->`);
        const each_array_2 = ensure_array_like(kindsFor(port));
        for (let $$index_1 = 0, $$length2 = each_array_2.length; $$index_1 < $$length2; $$index_1++) {
          let d = each_array_2[$$index_1];
          $$renderer2.push(`<button>+ ${escape_html(d.label)}</button>`);
        }
        $$renderer2.push(`<!--]--></div>`);
      }
      $$renderer2.push(`<!--]-->`);
    }
    $$renderer2.push(`<!--]--> `);
    if (loaded && descriptors.length === 0) {
      $$renderer2.push("<!--[0-->");
      $$renderer2.push(`<div class="empty">The engine reports no registered kinds.</div>`);
    } else {
      $$renderer2.push("<!--[-1-->");
    }
    $$renderer2.push(`<!--]-->`);
  });
}
export {
  _page as default
};
