# E2B sandbox template: Debian bookworm + VNC desktop + shared-PTY + dev toolkits.
#
# - Xvfb/fluxbox/x11vnc/noVNC expose a GUI over port 6080 (noVNC web UI).
# - A persistent `tmux -L main` session backs both the xterm inside the VNC
#   desktop and every interactive PTY launched by the E2B Go SDK, so bash
#   commands issued via `Pty.Create` are visible in the browser.
# - Chromium + Playwright/Puppeteer shared libs are pre-installed so headed
#   browser automation "just works" under DISPLAY=:0.
#
# Services are started by `start_cmd` in e2b.toml — envd ignores ENTRYPOINT/CMD.
FROM debian:bookworm

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    TZ=Etc/UTC \
    PIP_BREAK_SYSTEM_PACKAGES=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    PIP_NO_CACHE_DIR=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

# -- System base + common dev utilities ---------------------------------------
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl wget gnupg lsb-release \
        git openssh-client \
        sudo procps psmisc tini tzdata locales \
        jq less unzip zip tar xz-utils \
        vim-tiny nano \
        build-essential pkg-config make \
        tmux xauth xdg-utils \
    && sed -i 's/^# *\(C.UTF-8.*\)/\1/' /etc/locale.gen || true \
    && rm -rf /var/lib/apt/lists/*

# -- X stack + VNC + window manager ------------------------------------------
RUN apt-get update && apt-get install -y --no-install-recommends \
        xvfb x11-utils x11-xserver-utils \
        x11vnc novnc websockify \
        fluxbox xterm dbus-x11 \
    && mkdir -p -m 1777 /tmp/.X11-unix \
    && ln -sf /usr/share/novnc/vnc.html /usr/share/novnc/index.html \
    && rm -rf /var/lib/apt/lists/*

# -- Fonts (CJK + emoji so screenshots and web UIs don't show tofu) -----------
RUN apt-get update && apt-get install -y --no-install-recommends \
        fonts-liberation fonts-dejavu \
        fonts-noto-core fonts-noto-cjk fonts-noto-color-emoji \
    && rm -rf /var/lib/apt/lists/*

# -- Chromium + headed-browser shared libraries (covers Playwright too) -------
RUN apt-get update && apt-get install -y --no-install-recommends \
        chromium chromium-driver \
        libnss3 libgbm1 libasound2 libxss1 libxkbcommon0 \
        libgtk-3-0 libpango-1.0-0 libpangocairo-1.0-0 \
        libatk-bridge2.0-0 libatk1.0-0 libcups2 libdrm2 \
        libxcomposite1 libxdamage1 libxrandr2 libxfixes3 \
        libnotify4 libsecret-1-0 \
    && rm -rf /var/lib/apt/lists/*

# -- Python 3 + common automation libraries -----------------------------------
RUN apt-get update && apt-get install -y --no-install-recommends \
        python3 python3-pip python3-venv python3-dev python3-wheel pipx \
    && rm -rf /var/lib/apt/lists/* \
    && pip3 install --upgrade pip setuptools wheel \
    && pip3 install \
        requests httpx aiohttp \
        beautifulsoup4 lxml \
        pyyaml python-dotenv click typer rich tqdm \
        pydantic \
        playwright

# -- Node.js 20 (NodeSource) + common global tooling --------------------------
RUN curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/* \
    && npm install -g --no-audit --no-fund \
        pnpm yarn \
        typescript ts-node tsx \
        playwright puppeteer-core \
    && npm cache clean --force

# -- E2B runtime user (envd runs as uid 1000 "user") --------------------------
# The user may already exist in some base images; guard with id checks.
RUN id -u user >/dev/null 2>&1 || useradd --create-home --shell /bin/bash --uid 1000 user \
    && echo "user ALL=(ALL) NOPASSWD: ALL" > /etc/sudoers.d/user \
    && chmod 0440 /etc/sudoers.d/user \
    && mkdir -p /home/user/.config/fluxbox \
    && printf 'session.screen0.toolbar.visible: false\n' > /home/user/.config/fluxbox/init \
    && chown -R user:user /home/user

# -- Desktop bootstrap script (invoked by e2b.toml start_cmd) -----------------
RUN cat <<'SH' > /usr/local/bin/start-desktop.sh
#!/usr/bin/env bash
set -euo pipefail

export DISPLAY=:0
export XDG_RUNTIME_DIR=/tmp/runtime-user
export XAUTHORITY=/home/user/.Xauthority
mkdir -p -m 700 "$XDG_RUNTIME_DIR"
touch "$XAUTHORITY"
mkdir -p -m 1777 /tmp/.X11-unix

# 1) Shared tmux session first — readiness and PTY-mirroring depend on it.
#    Run as uid 1000 "user" so the socket lands in /tmp/tmux-1000/ and matches
#    the uid that the E2B Go SDK's Commands.Run / Pty.Create use by default.
sudo -u user -H tmux -L main kill-server 2>/dev/null || true
sudo -u user -H tmux -L main new-session -d -s main -x 200 -y 50 "bash -l"

# 2) Two virtual X servers: :0 hosts the terminal desktop, :1 hosts the
#    browser desktop. Separate displays let each get its own noVNC URL
#    without chromium stealing focus from the xterm and vice versa.
Xvfb :0 -screen 0 1440x900x24 -ac +extension RANDR -nolisten tcp &
Xvfb :1 -screen 0 1440x900x24 -ac +extension RANDR -nolisten tcp &
for d in :0 :1; do
    for _ in $(seq 1 50); do
        xdpyinfo -display "$d" >/dev/null 2>&1 && break
        sleep 0.1
    done
done

# 3) Minimal WM + session dbus on each display. dbus-launch is shared.
eval "$(dbus-launch --sh-syntax)"
DISPLAY=:0 fluxbox >/tmp/fluxbox.log 2>&1 &
DISPLAY=:1 fluxbox >/tmp/fluxbox.1.log 2>&1 &

# 4) Mirror the shared tmux session into the terminal desktop (:0). Attach
#    as "user" so the xterm talks to the same /tmp/tmux-1000/main socket.
sudo -u user -H DISPLAY=:0 XAUTHORITY="$XAUTHORITY" \
     xterm -geometry 160x40 -fa Monospace -fs 11 -T "e2b shared tmux" \
           -e tmux -L main attach-session -t main >/tmp/xterm.log 2>&1 &

# 5) Per-display VNC servers + noVNC bridges.
#    :0 -> 5900 -> 6080 (terminal URL)
#    :1 -> 5901 -> 6081 (browser URL)
x11vnc -display :0 -localhost -forever -shared -nopw -rfbport 5900 \
       -quiet >/tmp/x11vnc.log 2>&1 &
x11vnc -display :1 -localhost -forever -shared -nopw -rfbport 5901 \
       -quiet >/tmp/x11vnc.1.log 2>&1 &
websockify --web=/usr/share/novnc 0.0.0.0:6080 127.0.0.1:5900 \
       >/tmp/websockify.log 2>&1 &
websockify --web=/usr/share/novnc 0.0.0.0:6081 127.0.0.1:5901 \
       >/tmp/websockify.1.log 2>&1 &

# Exit if any background service dies so envd can surface the failure.
wait -n
SH
RUN chmod +x /usr/local/bin/start-desktop.sh

# -- Profile hook: SDK-launched bash PTYs auto-attach to the shared tmux ------
RUN cat <<'SH' > /etc/profile.d/e2b-tmux.sh
# Attach interactive login shells to the shared tmux session so the bash
# session is mirrored into the VNC desktop. Opt out with E2B_NO_TMUX=1.
if [ -z "${TMUX:-}" ] && [ -z "${E2B_NO_TMUX:-}" ] \
   && [ -t 0 ] && [ -t 1 ] \
   && [ "${SHLVL:-1}" = "1" ] \
   && command -v tmux >/dev/null 2>&1 \
   && tmux -L main has-session -t main 2>/dev/null; then
    exec tmux -L main attach-session -t main
fi
SH

USER user
WORKDIR /home/user
ENV HOME=/home/user \
    DISPLAY=:0 \
    XDG_RUNTIME_DIR=/tmp/runtime-user \
    PATH=/home/user/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    NODE_PATH=/usr/lib/node_modules
