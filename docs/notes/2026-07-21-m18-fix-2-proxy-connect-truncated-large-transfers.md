# M18 fix 2 — proxy CONNECT truncated large transfers

Live: git push failed "Recv failure: Connection reset by peer" (routing
was now correct — the error moved from a firewall timeout to a proxy-side
reset). Bug: handleConnect's bidirectional io.Copy tore down BOTH sockets
the instant either direction ended, RSTing an in-flight transfer in the
other direction (clone downloads and survives; push uploads more and
trips it). Fix: half-close — each direction CloseWrite()s its own write
side on EOF and the full close waits for BOTH copies to finish; plus
forward any TLS bytes the client pipelined into the hijack buffer before
the 200. Regression test: 2 MiB bidirectional round-trip through the
tunnel.
