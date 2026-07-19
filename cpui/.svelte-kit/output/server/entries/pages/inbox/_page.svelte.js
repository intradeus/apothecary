import { e as escape_html, b as ensure_array_like, a as attr_class, d as attr } from "../../../chunks/index.js";
import { a as ago } from "../../../chunks/api.js";
function _page($$renderer, $$props) {
  $$renderer.component(($$renderer2) => {
    let pings = [];
    let feedback = {};
    let busy = {};
    const gateTypes = ["plan_approval", "final_review"];
    $$renderer2.push(`<h1>Inbox</h1> `);
    {
      $$renderer2.push("<!--[-1-->");
    }
    $$renderer2.push(`<!--]--> `);
    if (pings.length === 0) {
      $$renderer2.push("<!--[0-->");
      $$renderer2.push(`<div class="empty">${escape_html("Loading…")}</div>`);
    } else {
      $$renderer2.push("<!--[-1-->");
      $$renderer2.push(`<!--[-->`);
      const each_array = ensure_array_like(pings);
      for (let $$index = 0, $$length = each_array.length; $$index < $$length; $$index++) {
        let p = each_array[$$index];
        $$renderer2.push(`<div class="card"><div class="row spread"><span${attr_class(`pingtype ${gateTypes.includes(p.type) ? "wanted" : "unwanted"}`)}>${escape_html(p.type)}</span> <span class="muted small">${escape_html(ago(p.raised_at))} · <a${attr("href", `/runs/${p.run_id}`)}>view run</a></span></div> <p style="margin:8px 0">${escape_html(p.question)}</p> `);
        if (p.context) {
          $$renderer2.push("<!--[0-->");
          $$renderer2.push(`<p class="muted small" style="white-space:pre-wrap">${escape_html(p.context)}</p>`);
        } else {
          $$renderer2.push("<!--[-1-->");
        }
        $$renderer2.push(`<!--]--> `);
        if (p.payload_ref) {
          $$renderer2.push("<!--[0-->");
          $$renderer2.push(`<p class="mono small">`);
          if (p.payload_ref.startsWith("http")) {
            $$renderer2.push("<!--[0-->");
            $$renderer2.push(`<a${attr("href", p.payload_ref)} target="_blank" rel="noreferrer">${escape_html(p.payload_ref)} ↗</a>`);
          } else {
            $$renderer2.push("<!--[-1-->");
            $$renderer2.push(`${escape_html(p.payload_ref)}`);
          }
          $$renderer2.push(`<!--]--></p>`);
        } else {
          $$renderer2.push("<!--[-1-->");
        }
        $$renderer2.push(`<!--]--> `);
        if (gateTypes.includes(p.type)) {
          $$renderer2.push("<!--[0-->");
          $$renderer2.push(`<textarea placeholder="Feedback (required when rejecting)">`);
          const $$body = escape_html(feedback[p.ping_id]);
          if ($$body) {
            $$renderer2.push(`${$$body}`);
          }
          $$renderer2.push(`</textarea> <div class="row" style="margin-top:10px"><button class="approve"${attr("disabled", busy[p.ping_id], true)}>Approve</button> <button class="reject"${attr("disabled", busy[p.ping_id] || !feedback[p.ping_id]?.trim(), true)}>Reject with feedback</button></div>`);
        } else {
          $$renderer2.push("<!--[-1-->");
          $$renderer2.push(`<textarea placeholder="Your answer to the agent…">`);
          const $$body_1 = escape_html(feedback[p.ping_id]);
          if ($$body_1) {
            $$renderer2.push(`${$$body_1}`);
          }
          $$renderer2.push(`</textarea> <div class="row" style="margin-top:10px"><button class="primary"${attr("disabled", busy[p.ping_id] || !feedback[p.ping_id]?.trim(), true)}>Send answer</button></div>`);
        }
        $$renderer2.push(`<!--]--></div>`);
      }
      $$renderer2.push(`<!--]-->`);
    }
    $$renderer2.push(`<!--]-->`);
  });
}
export {
  _page as default
};
