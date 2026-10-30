// glocker KWin usage bridge (Plasma 6 / KWin on Wayland).
//
// KWin blocks unprivileged clients from reading the focused window, and a KWin
// script — unlike a GNOME Shell extension — cannot EXPORT a D-Bus interface, only
// call out (callDBus). So the glocker daemon owns the name app.glocker.KWin on
// the user's session bus and exports a Push(json) method; this script pushes the
// window list to it on every change. The daemon returns the last push from
// KWinSource.Capture(). See internal/usage/kwin.go.
//
// The pushed JSON is [{class, instance, title, active}] — the shape
// usage.Sample expects (same as the GNOME extension's GetWindows).

const SERVICE = "app.glocker.KWin";
const PATH = "/app/glocker/KWin";
const IFACE = "app.glocker.KWin";
const METHOD = "Push";

// trackable drops the shell's own surfaces (panels, desktop, OSDs, menus) so the
// sample carries real application windows, like the GNOME bridge.
function trackable(w) {
    if (!w) return false;
    if (w.desktopWindow || w.dock || w.splash || w.notification ||
        w.criticalNotification || w.onScreenDisplay || w.toolbar || w.menu ||
        w.dropdownMenu || w.popupMenu || w.tooltip || w.comboBox) {
        return false;
    }
    return ("" + (w.resourceClass || "")) !== "";
}

// A window stays in workspace.windowList() for a moment after it closes, and a
// trailing windowActivated (focus moving off it) can re-snapshot that stale list
// — so excluding it from just the removal push isn't enough; a closed window
// would linger. Instead remember removed windows and filter them from every
// snapshot. Bounded so the list can't grow without limit over a long session.
const removed = [];
const removedMax = 64;

function wasRemoved(w) {
    for (let i = 0; i < removed.length; i++) {
        if (removed[i] === w) return true;
    }
    return false;
}

function snapshot() {
    const active = workspace.activeWindow;
    const out = [];
    const list = workspace.windowList();
    for (let i = 0; i < list.length; i++) {
        const w = list[i];
        if (!trackable(w) || wasRemoved(w)) continue;
        out.push({
            class: "" + (w.resourceClass || ""),
            instance: "" + (w.resourceName || ""),
            title: "" + (w.caption || ""),
            active: active !== null && w === active,
        });
    }
    return JSON.stringify(out);
}

function push() {
    try {
        callDBus(SERVICE, PATH, IFACE, METHOD, snapshot());
    } catch (e) {
        // The daemon may not own the name yet (not started, or re-selecting a
        // backend). The next window event pushes again, so state self-heals.
    }
}

// Re-push whenever the snapshot could change. windowRemoved also records the
// window so later snapshots drop it even if it briefly survives in windowList().
workspace.windowActivated.connect(push);
workspace.windowRemoved.connect(function (w) {
    removed.push(w);
    if (removed.length > removedMax) removed.shift();
    push();
});
workspace.windowAdded.connect(function (w) {
    if (w && w.captionChanged) w.captionChanged.connect(push);
    push();
});

// Hook title changes on windows that already exist, then push the initial state.
const existing = workspace.windowList();
for (let i = 0; i < existing.length; i++) {
    const w = existing[i];
    if (w && w.captionChanged) w.captionChanged.connect(push);
}
push();
