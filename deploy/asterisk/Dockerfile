# Asterisk for the local Westbridge test stand.
#
# There is no official Asterisk image, and the unofficial ones on Docker Hub
# vary in config layout and are mostly amd64-only. Ubuntu 24.04 LTS ships
# Asterisk 20 (app_confbridge + chan_pjsip) for both amd64 and arm64, which is
# what makes this build work unchanged on an Apple Silicon laptop.
FROM ubuntu:24.04

ARG DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
    && apt-get install -y --no-install-recommends asterisk \
    && rm -rf /var/lib/apt/lists/*

# chan_sip is deprecated in Asterisk 20 and would fight chan_pjsip over
# UDP/5060 if it ever autoloaded. modules.conf ends inside its [modules]
# section, so appending puts the directive in the right category.
RUN printf '\n; Westbridge stand: PJSIP only.\nnoload => chan_sip.so\n' >> /etc/asterisk/modules.conf \
    && rm -f /etc/asterisk/sip.conf

# The package relies on a tmpfiles rule for this directory, which never runs
# in a container.
RUN mkdir -p /var/run/asterisk && chown asterisk:asterisk /var/run/asterisk

EXPOSE 5038/tcp 5060/udp 10000-10100/udp

STOPSIGNAL SIGTERM

# -f keeps Asterisk in the foreground as PID 1; the console verbosity is what
# makes "docker compose logs -f asterisk" useful while testing.
CMD ["asterisk", "-f", "-vvv", "-U", "asterisk", "-G", "asterisk"]
