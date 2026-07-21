// Apothecary in-container relay: workspace-loopback TCP -> the unix
// sockets bind-mounted at /opt/apothecary, which the engine-side adapter
// pumps to its own loopback ports. This is what gives the devcontainer
// provisioner loopback parity with the coder reverse tunnel: the shim and
// proxy env keep their invariant http://127.0.0.1:<port> addresses, and no
// container->host IP networking is assumed (hardened docker hosts drop
// it). Started detached (`docker exec -d node relay.mjs 8080:/opt/...`)
// at provision/reattach; per-connection piping, no protocol.
//
// EADDRINUSE means a live relay from a previous provision already owns the
// port: exit quietly and let it serve. (If the engine's ports changed
// across a restart, the container is restarted too, which clears both the
// old relay and its listeners.)
import net from 'node:net';

for (const pair of process.argv.slice(2)) {
  const i = pair.indexOf(':');
  const port = Number(pair.slice(0, i));
  const sock = pair.slice(i + 1);
  const srv = net.createServer((c) => {
    const u = net.connect(sock);
    const done = () => { c.destroy(); u.destroy(); };
    c.pipe(u); u.pipe(c);
    c.on('error', done); u.on('error', done);
    c.on('close', done); u.on('close', done);
  });
  srv.on('error', (e) => {
    if (e.code === 'EADDRINUSE') process.exit(0);
    throw e;
  });
  srv.listen(port, '127.0.0.1');
}
