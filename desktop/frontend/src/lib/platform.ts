import { System } from "@wailsio/runtime";

// The run-shortcut label ("⌘↵" / "Ctrl↵"): the KEY HANDLERS already accept
// both modifiers (`e.metaKey || e.ctrlKey`), so this only controls what the
// button/hint text shows. `System.IsMac()` reads the OS Wails' own runtime
// injects at startup (window._wails.environment), so — unlike sniffing
// navigator.platform/userAgent — it reports the actual host OS rather than
// whatever the embedded WebView happens to claim.
export const MOD_KEY = System.IsMac() ? "⌘" : "Ctrl";
export const RUN_SHORTCUT = `${MOD_KEY}↵`;
