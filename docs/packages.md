# What the images ship, and why

Two variants come out of one Dockerfile. **Lite** is the default: the desktop
plus the tools somebody needs to work in it. **Full** adds what is too large or
too specialised to hand everybody.

```bash
make up            # builds both, tagged with the version
make image-lite    # only lite
make image-full    # only full
```

```
sentineldesk:<version>-lite    also :latest, :lite
sentineldesk:<version>-full    also :full
```

Full is built `FROM` lite, so the two share every layer up to that point: a
registry holding both stores the difference once, and a machine that already
pulled lite pulls only the extra.

**Lite here does not mean headless.** On a Raspberry Pi, "Lite" means no
desktop at all. Here the desktop *is* the product, so lite means the smallest
set that leaves nobody reaching for `apt` on their first afternoon.

## Where the lists live

Not in the Dockerfile. Dockerfile has no `include`, and a forty-line `RUN` with
the reasoning wedged into shell comments is a list nobody edits twice.

```
deploy/packages/desktop.txt      lite
deploy/packages/full.txt         the extra in full, every architecture
deploy/packages/full-amd64.txt   the extra that only exists on amd64
```

One package per line, `#` for comments, blank lines ignored. Changing what the
desktop ships means editing a text file. Every size in those files is measured —
`apt-cache Installed-Size` summed over the package and everything it drags in —
not estimated.

`full-amd64.txt` is separate so an arm64 build does not fail on a package name
the archive has never carried.

## The rule

Anything past about **50 MB** has to be something most people would miss.
Below that, if it is useful more than once a month, it goes in lite.

Two exceptions were made deliberately, and both are named where they live:
`git` at 99 MB, because a desktop people are meant to work in without it is a
desktop they leave; and `nmap` at 27 MB, because reaching other machines is
what this desktop is usually for.

## What lite carries

| Group | Packages | Why |
|---|---|---|
| Shell | `xfce4-panel` `xfce4-whiskermenu-plugin` `xfdesktop4` `librsvg2-bin` | Whisker menu, numbered workspaces, one wallpaper per workspace |
| File manager | `thunar` `tumbler` `tumbler-plugins-extra` | Thunar finished the XFCE move: split view (F3), configurable toolbar, bulk rename; tumbler draws its thumbnails and is a Recommends the build would otherwise drop |
| Panel plugins | `xfce4-pulseaudio-plugin` `xfce4-clipman-plugin` `xfce4-cpugraph-plugin` `xfce4-xkb-plugin` `xfce4-genmon-plugin` `xfce4-eyes-plugin` | genmon is the SentinelDesk status line: ● REC, agent jobs |
| Notifications | `xfce4-notifyd` `libnotify-bin` | toasts a person actually sees; `notify-send` for scripts |
| Widgets | `conky-all` `xpad` | the system card and sticky notes; notes persist as files the agent can read |
| Theme | `adwaita-icon-theme` `papirus-icon-theme` `fonts-roboto` `fonts-noto-core` `fonts-jetbrains-mono` | |
| Core | `chromium` `lxterminal` `zenity` `dbus-x11` `vlc` `thunderbird` | VLC doubles as a way to test the upstream audio path; mail is in lite by decision — its size rule exception is argued in desktop.txt |
| Remote out | `tigervnc-viewer` `freerdp3-x11` | This desktop is often the jump host, not the destination |
| Graphics | `libgl1-mesa-dri` `mesa-vulkan-drivers` `vulkan-tools` | `vulkaninfo` answers "am I on llvmpipe?" |
| Utilities | `mousepad` `gpicview` `xarchiver` `galculator` `lxtask` `evince` `zathura` `zathura-pdf-poppler` `ffmpegthumbnailer` | the thumbnailer also answers "one frame of this recording" from a shell |
| Network | `nmap` `dnsutils` `iperf3` `tcpdump` `mtr-tiny` `traceroute` `socat` `whois` `sngrep` `net-tools` `iputils-ping` `ethtool` `openvpn` | |
| Terminal | `git` `curl` `wget` `vim` `tmux` `less` `tree` `ncdu` `pv` `jq` `rsync` `unzip` `zip` `p7zip-full` | |
| System | `bash-completion` `man-db` `psmisc` `usbutils` `strace` `ffmpeg` `scrot` `avahi-daemon` `ntfs-3g` `cifs-utils` `python3-pip` | |
| Locale | `tzdata` `xkb-data` | Read at start from `TZ` and `KEYBOARD_LAYOUT` |

Some of these earn their place for reasons specific to this product rather than
general usefulness:

- **`iperf3`** — 0.4 MB on a product whose whole pitch is throughput and
  latency. It is the tool that answers "is it the network or is it me?".
- **`ffmpeg`** — this desktop records and streams; ffmpeg is how you inspect,
  convert or repair what it produced without leaving it.
- **`vulkan-tools`** — the first question when video looks slow is whether
  acceleration is working at all.
- **`avahi-daemon`** — machines answer to `name.local`, which matters on a
  desktop whose job is often reaching the rest of its network.
- **`sngrep`** — 0.3 MB, and it turns "the call failed" into knowing which leg
  dropped it.

## What full adds

| Package | Size | Why it is not in lite |
|---|---:|---|
| `libreoffice` | 678 MB | What "full" means to most people. On its own it is most of the difference between the images. |
| `firefox-esr` | 521 MB | Not redundancy — a different engine. "Works in Chromium" and "works in Firefox" are two answers. |
| `wine` | 733 MB | amd64 only, and needs the i386 multiarch that lite deliberately does not carry. |
| `steam-installer` | 390 MB | amd64 only. It is what forces non-free components, i386 and a pre-accepted licence. |
| `wireshark` | 340 MB | `tcpdump` in lite covers *taking* a capture. This is for *reading* one. |
| `build-essential` | 297 MB | Raspberry Pi OS puts this in its base, because a Pi is a machine people learn to program on. This is a desktop people work from. |
| `gimp` | 276 MB | Lite views images; this edits them. |
| `golang-go` | 246 MB | This project is written in Go, so the temptation is to call it a base tool. A quarter of a gigabyte for people who will never type `go` says otherwise: `git` is in lite because everybody clones something, a toolchain is not. |
| `gdb` `python3-numpy` `meson` `geany` | 81 MB | Rounds out the build set. |
| `hunspell-es` | small | Spell checking past `en_US`, which is all lite already carries. It follows LibreOffice rather than standing on its own. |

## Measured against Raspberry Pi OS

Checked against pi-gen — `stage2` (base), the `rpd-*` meta-packages that make up
the desktop, and `stage5` (Full).

**Where we already agreed.** `rpd-utilities`, Raspberry Pi's own utility set,
is `evince`, `galculator`, `lxtask`, `mousepad`, `xarchiver` — the same five
chosen here before the comparison was made. Their image viewer is `eom` (Eye of
MATE) where this uses `gpicview`, which is lighter and from the same family as
the rest of the shell.

**What was taken from their base.** `bash-completion`, `man-db`, `psmisc`,
`usbutils`, `strace`, `ethtool`, `ntfs-3g`, `cifs-utils`, `ffmpeg`, `scrot` —
about 32 MB in total, and every one of them is something whose absence is
noticed within the hour.

**What was deliberately not taken.** A large part of Raspberry Pi OS exists
because a Pi is a physical single-board computer, and none of it transfers to a
container:

- `rpi-imager`, `piclone`, `piwiz`, `rpi-eeprom`, `raspi-utils`, `rpi-update` —
  imaging, cloning and firmware for hardware that is not here.
- `gpiod`, `python3-gpiozero`, `python3-spidev`, `python3-smbus2`, `sense-hat`,
  `python3-picamera2`, `rpicam-apps` — GPIO, SPI, I²C and camera. There are no
  pins.
- `bluez`, `modemmanager`, `usb-modeswitch` — no Bluetooth radio, no USB modem.
- `parted`, `dosfstools`, `fbset` — disks and framebuffers of a real machine.
- `realvnc-vnc-server`, `wayvnc`, `rpi-connect` — remote *access servers*.
  SentinelDesk is that layer; a second one inside it would be confusing at best.

**What was left out on purpose despite being in their Full.** `scratch3`,
`mu-editor`, `code-the-classics`, `thonny` — Raspberry Pi's mission is teaching
people to program, and those exist to serve it. This desktop has a different
job. `kicad` at 390 MB is PCB design, too specialised to carry for everyone.
`claws-mail` is only 5 MB but an email client on a remote desktop is a thing
almost nobody wants.

## What is not there and cannot be

- **WireGuard.** The Debian `wireguard` package is 118 MB of `dracut`,
  `initramfs-tools` and `kmod` — kernel-module machinery that cannot do
  anything inside a container. `wireguard-tools` (the userspace half) would
  work, and can be added if the host provides the module.
- **Ghidra.** Not packaged in Debian at all. It needs a JDK plus roughly a
  gigabyte, installed by hand from its own release.

## Timezone and keyboard

One image, every region. Both are read at start, so neither is baked in.

```yaml
environment:
  - TZ=America/Argentina/Buenos_Aires    # any tzdata name
  - KEYBOARD_LAYOUT=latam                # us · es (Spain) · latam · pt · fr · de…
  - KEYBOARD_VARIANT=                    # optional, passed through untouched
```

`TZ` is applied by `entrypoint.sh` as root; `KEYBOARD_LAYOUT` by
`desktop-init.sh` against the running X server. An unknown value is reported and
ignored rather than half-applied — a clock quietly running in UTC because of a
typo is worse than one that says so.

## VPN clients need more than a package

`openvpn` is installed in lite, and the compose files grant what it needs to
actually work:

```yaml
cap_add:
  - NET_ADMIN
devices:
  - /dev/net/tun
```

Without both, `openvpn` installs cleanly and then fails at the moment somebody
needs it. `NET_ADMIN` is a real grant — it lets the container manage its own
interfaces, routes and firewall — so both lines are worth removing on a
deployment that never dials a VPN.

The graphical client Debian offers, `network-manager-openvpn-gnome`, needs
NetworkManager running. Nothing manages the network in this container, so it
would install a menu entry that opens a window that cannot connect.
`openvpn-connect` is used instead: a profile picker in the same shape as
`vnc-connect` and `rdp-connect`.

## The menu

Grouped by what somebody wants to do, not by which package ships it. Terminal,
Web Browser, Files and Text Editor stay one click away because they are what
gets opened every session; the rest is one level down.

```
Terminal · Web Browser · Files · Text Editor
├── Accessories    image viewer · document viewer · archives · calculator · media
├── Network        RDP · VNC · VPN · sngrep · nmap · iperf3 · mtr
├── System Tools   task manager · htop · ncdu · Midnight Commander
└── All Applications
```

**Every entry runs something the image installs.** The menu previously offered
FileZilla, which was never in the package list — a menu that lies is worse than
a short one.

The command-line entries under Network open a terminal with the tool's usage
already printed (`sentineldesk-hint`), because a menu item that drops you at a
bare prompt has not saved you the part you were going to look up.
