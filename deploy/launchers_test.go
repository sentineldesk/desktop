// Copyright 2026 Federico Pereira <fpereira@cnsoluciones.com>
//
// Licensed under the Apache License, Version 2.0.
//
// This product's name and logo are trademarks of Federico Pereira and are not
// covered by the license above. See the README for the trademark policy.
//
// SPDX-License-Identifier: Apache-2.0

// The panel's launchers, checked against the panel.
//
// The bug these guard against had no error anywhere, and it predates the
// panel that is here now. lxpanel's launchbar named commands inline, could
// not persist that, and so invented a desktop entry per button at every start
// — random name, mode 0600, no executable bit. libfm then refused to trust it
// and asked "this text file 'Files' seems to be an executable script, what do
// you want to do with it?" on every click of every launcher, while three
// fresh orphans accumulated per boot in a persistent home.
//
// Every layer reported success. The panel drew, the file existed, the click
// was received. Only the person clicking knew.
//
// xfce4-panel replaced lxpanel (2026-08-19) and moved the failure, not
// removed it: its launcher plugin reads desktop entries from a private
// per-plugin directory (~/.config/xfce4/panel/launcher-<id>/), which the
// Dockerfile has to populate for every launcher the panel names. A plugin
// whose directory is missing draws an empty button that does nothing —
// silently, which is the class of bug this file exists to keep out.
package deploy

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const launcherDir = "desktop/launchers"

// Entries the panel may name that this repository does not ship: they come
// from a Debian package, and the Dockerfile copies them out of the installed
// system instead. Growing this list is a decision, not a convenience — each
// name here is a file the build breaks on if the package ever drops it.
var packageProvided = map[string]string{
	"galculator.desktop": "the galculator package; xfce4-calculator-plugin left Debian 13",
}

func panelConfig(t *testing.T) string {
	t.Helper()
	raw, err := FS.ReadFile("desktop/xfce4-panel.xml")
	if err != nil {
		t.Fatalf("the panel config is not in the embedded tree: %v", err)
	}
	return string(raw)
}

func launcherFiles(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, err := fs.ReadDir(FS, launcherDir)
	if err != nil {
		t.Fatalf("%s is not in the embedded tree: %v", launcherDir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		raw, err := FS.ReadFile(launcherDir + "/" + e.Name())
		if err != nil {
			t.Errorf("%s: %v", e.Name(), err)
			continue
		}
		out[e.Name()] = string(raw)
	}
	if len(out) == 0 {
		t.Fatal("no launchers: this test would pass by checking nothing")
	}
	return out
}

// key reads a Desktop Entry field, ignoring the comment block above it.
func key(entry, name string) string {
	for _, line := range strings.Split(entry, "\n") {
		s := strings.TrimSpace(line)
		if strings.HasPrefix(s, "#") {
			continue
		}
		if v, ok := strings.CutPrefix(s, name+"="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// A launcher plugin block in xfce4-panel.xml: its id, then its items.
var launcherPlugin = regexp.MustCompile(
	`(?s)<property name="plugin-(\d+)" type="string" value="launcher">(.*?)</property>\s*</property>`)

var itemValue = regexp.MustCompile(`<value type="string" value="([^"]+\.desktop)"/>`)

// launcherItems maps plugin id -> the desktop entries that launcher names.
func launcherItems(t *testing.T) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, m := range launcherPlugin.FindAllStringSubmatch(panelConfig(t), -1) {
		out[m[1]] = nil
		for _, v := range itemValue.FindAllStringSubmatch(m[2], -1) {
			out[m[1]] = append(out[m[1]], v[1])
		}
	}
	if len(out) == 0 {
		t.Fatal("the panel names no launcher plugin, which means the buttons " +
			"this file guards do not exist — or the config's shape changed " +
			"and this test is now checking nothing")
	}
	return out
}

// Every button points at an entry that exists — ours in desktop/launchers/,
// or a package's on the explicit list above. A name that is right in the
// config and absent from the image is a button that does nothing, with the
// panel still drawing it.
func TestEveryPanelButtonNamesALauncherWeShip(t *testing.T) {
	files := launcherFiles(t)
	for id, items := range launcherItems(t) {
		if len(items) == 0 {
			t.Errorf("launcher plugin-%s has no items: an empty button", id)
		}
		for _, item := range items {
			if strings.Contains(item, "/") {
				t.Errorf("plugin-%s names %s by path — the plugin resolves bare "+
					"names in its own directory, and a path is the shape that "+
					"stops being true on the next refactor", id, item)
				continue
			}
			if _, ok := files[item]; ok {
				continue
			}
			if _, ok := packageProvided[item]; ok {
				continue
			}
			t.Errorf("the panel points at %s and this repository has no such launcher", item)
		}
	}
}

// The Dockerfile populates every launcher plugin's private directory. This is
// the xfce4 shape of the old orphan bug: the plugin only reads
// ~/.config/xfce4/panel/launcher-<id>/, so a COPY that lands anywhere else —
// or nowhere — leaves an empty button and no error.
func TestTheDockerfileFillsEveryLauncherDirectory(t *testing.T) {
	raw, err := FS.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	df := string(raw)
	for id, items := range launcherItems(t) {
		dir := "launcher-" + id + "/"
		for _, item := range items {
			// Either an explicit COPY of the item into the directory, or a RUN
			// cp for package-provided entries. Both name the directory and the
			// file on one line.
			found := false
			for _, l := range strings.Split(df, "\n") {
				if strings.Contains(l, dir) && strings.Contains(l, item) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("nothing in the Dockerfile puts %s into %s — that button "+
					"will draw empty and click into nothing", item, dir)
			}
		}
	}
}

// And the other direction: a launcher nobody references is dead weight in the
// image, and usually means a rename landed on one side only.
func TestEveryLauncherWeShipIsOnThePanel(t *testing.T) {
	cfg := panelConfig(t)
	for name := range launcherFiles(t) {
		if !strings.Contains(cfg, name) {
			t.Errorf("%s is shipped and the panel never names it", name)
		}
	}
}

// A desktop entry missing any of these is one the desktop will not run.
func TestEveryLauncherIsAValidDesktopEntry(t *testing.T) {
	for name, entry := range launcherFiles(t) {
		if !strings.Contains(entry, "[Desktop Entry]") {
			t.Errorf("%s has no [Desktop Entry] header", name)
			continue
		}
		for _, k := range []string{"Type", "Name", "Exec", "Icon"} {
			if key(entry, k) == "" {
				t.Errorf("%s has no %s=", name, k)
			}
		}
		if got := key(entry, "Type"); got != "Application" {
			t.Errorf("%s is Type=%q, which no launcher will start", name, got)
		}
	}
}

// Icons are absolute paths, for the reason the old panel config explained at
// length: on Raspberry Pi OS, resolving a themed icon lands in
// gtk_icon_theme_choose_icon_for_scale and asserts, and the button draws blank.
func TestLauncherIconsAreAbsolutePaths(t *testing.T) {
	for name, entry := range launcherFiles(t) {
		icon := key(entry, "Icon")
		if !strings.HasPrefix(icon, "/") {
			t.Errorf("%s has Icon=%q. A themed name draws nothing on Raspberry Pi OS; "+
				"use the path.", name, icon)
		}
	}
}

// The one that matters most, and the one a tidy-up would remove first.
//
// The trusted copies in /usr/share/applications — what the menu, whisker's
// favorites and the file manager resolve — must carry the executable bit: a
// COPY without --chmod=0755 reproduces the original bug exactly. The copies
// into the launcher plugins' private directories are exempt on purpose; the
// plugin reads those as its own configuration and mode plays no part.
func TestTheDockerfileInstallsLaunchersExecutable(t *testing.T) {
	raw, err := FS.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	var line string
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.Contains(l, "desktop/launchers") &&
			strings.Contains(l, "/usr/share/applications") &&
			strings.HasPrefix(strings.TrimSpace(l), "COPY") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatal("the Dockerfile never copies desktop/launchers/ into " +
			"/usr/share/applications, so the menu points at entries that do " +
			"not exist in the image")
	}
	if !strings.Contains(line, "--chmod=0755") {
		t.Errorf("the launchers are copied without --chmod=0755:\n  %s\n\n"+
			"libfm will not trust a launcher without the executable bit, and every "+
			"click will ask what to do with it — which is the bug this file exists to end.", line)
	}
}

// The orphans already on people's disks have to be swept up by something, or
// the fix only helps installations that did not have the problem.
func TestDesktopInitRemovesTheOldOrphans(t *testing.T) {
	raw, err := FS.ReadFile("desktop/desktop-init.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "lxpanel-launcher-") {
		t.Error("desktop-init.sh does not clean up lxpanel-launcher-*.desktop. " +
			"Homes that already collected them keep them forever: the home is a " +
			"persistent volume and nothing else ever looks in there.")
	}
}

// Whatever is in the repository is what the image gets. A launcher edited on
// disk and not embedded would ship stale through the embedded tree.
func TestTheEmbeddedLaunchersMatchTheDirectory(t *testing.T) {
	embedded := launcherFiles(t)
	entries, err := os.ReadDir(launcherDir)
	if err != nil {
		t.Fatalf("cannot read %s: %v", launcherDir, err)
	}
	seen := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		seen++
		want, err := os.ReadFile(filepath.Join(launcherDir, e.Name()))
		if err != nil {
			t.Errorf("%s: %v", e.Name(), err)
			continue
		}
		got, ok := embedded[e.Name()]
		if !ok {
			t.Errorf("%s is on disk and not in the binary", e.Name())
			continue
		}
		if got != string(want) {
			t.Errorf("%s differs between the repository and the binary", e.Name())
		}
	}
	if seen != len(embedded) {
		t.Errorf("%d launchers on disk, %d embedded", seen, len(embedded))
	}
}
