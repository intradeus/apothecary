# The assistant pod's image (docs/assistant-plan.md): what a person's
# assistant sessions run on, and nothing more. git and ripgrep for the
# sessions and their git tool; libstdc++ for the bun-built harnesses; the musl
# loader's link, which a session's Landlock rules name. The harnesses arrive
# from the tools image and the shim from the shim image, each seeded into the
# pod at start (internal/adapters/assistant), so this image builds on its own.
#
# No shell: busybox is every applet, sh among them, so it goes, and with it
# every link to it (busybox's own find has no -lname: each link is read). The pod's tunnel container has the gateway image's shell;
# nothing here does. Build context: this directory.
FROM alpine:3.21
RUN apk add --no-cache git ripgrep libstdc++ \
 && mkdir -p /opt/apothecary \
 && ln -s "$(ls /lib/ld-musl-*.so.1)" /opt/apothecary/ld \
 && B=/bin/busybox \
 && for f in $($B find / -xdev -type l); do \
      case "$($B readlink "$f")" in *busybox) $B rm -f "$f" ;; esac; done \
 && $B rm -f $B
USER 1000:1000
