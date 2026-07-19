import { b as ensure_array_like, e as escape_html, d as attr } from "../../../chunks/index.js";
function _page($$renderer, $$props) {
  $$renderer.component(($$renderer2) => {
    let entries = [];
    let homeserver = "https://matrix.org";
    let accessToken = "";
    let roomId = "";
    let busy = false;
    $$renderer2.push(`<h1>Notification channels</h1> <p class="muted small" style="margin-top:-8px">One-way mirrors of the inbox — pings are answered here in the CPUI, never in the channel.</p> `);
    const each_array = ensure_array_like(entries);
    if (each_array.length !== 0) {
      $$renderer2.push("<!--[-->");
      for (let $$index = 0, $$length = each_array.length; $$index < $$length; $$index++) {
        let c = each_array[$$index];
        $$renderer2.push(`<div class="card row spread"><div><strong>${escape_html(c.kind)}</strong> <div class="mono small muted">${escape_html(c.kind === "matrix" ? `${c.config.homeserver} · ${c.config.room_id}` : JSON.stringify(c.config))}</div></div> <button class="danger">Remove</button></div>`);
      }
    } else {
      $$renderer2.push("<!--[!-->");
      $$renderer2.push(`<div class="empty">No channels configured.</div>`);
    }
    $$renderer2.push(`<!--]--> <form class="card"><h2>Add Matrix channel</h2> <label for="c-hs">Homeserver</label> <input id="c-hs"${attr("value", homeserver)} required="" placeholder="https://matrix.org"/> <label for="c-token">Access token</label> <input id="c-token" type="password"${attr("value", accessToken)} required="" placeholder="syt_…"/> <label for="c-room">Room ID</label> <input id="c-room"${attr("value", roomId)} required="" placeholder="!room:matrix.org"/> `);
    {
      $$renderer2.push("<!--[-1-->");
    }
    $$renderer2.push(`<!--]--> <div style="margin-top:14px"><button class="primary" type="submit"${attr("disabled", busy, true)}>Add channel</button></div></form>`);
  });
}
export {
  _page as default
};
