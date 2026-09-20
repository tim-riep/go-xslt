import { useEffect, useRef, type CSSProperties } from "react";
import { useAppDispatch, useAppState } from "../state/store";

/** Mounted once at the app root. Renders `ui.menu` at its recorded (x, y) —
 * dismissed by a click anywhere else, Escape, or scroll (its position would
 * otherwise drift out from under the pointer). */
export function ContextMenuHost() {
  const { ui } = useAppState();
  const dispatch = useAppDispatch();
  const ref = useRef<HTMLDivElement>(null);
  const menu = ui.menu;

  useEffect(() => {
    if (!menu) return;
    const close = () => dispatch({ type: "MENU_CLOSE" });
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && close();
    // mousedown (not click) so the same click that right-clicked a *different*
    // row doesn't first close this menu and then immediately reopen a new one
    // out of sync with React's event batching.
    window.addEventListener("mousedown", close);
    window.addEventListener("keydown", onKey);
    window.addEventListener("scroll", close, true);
    window.addEventListener("blur", close);
    return () => {
      window.removeEventListener("mousedown", close);
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("scroll", close, true);
      window.removeEventListener("blur", close);
    };
  }, [menu, dispatch]);

  if (!menu) return null;

  const style: CSSProperties = { left: menu.x, top: menu.y };
  // Keep the menu on-screen: flip above/left if it would overflow.
  if (typeof window !== "undefined") {
    const approxHeight = menu.items.length * 28 + 8;
    const approxWidth = 200;
    if (menu.y + approxHeight > window.innerHeight) style.top = Math.max(4, menu.y - approxHeight);
    if (menu.x + approxWidth > window.innerWidth) style.left = Math.max(4, menu.x - approxWidth);
  }

  return (
    <div className="context-menu" style={style} ref={ref} onMouseDown={(e) => e.stopPropagation()}>
      {menu.items.map((item, i) => (
        <div key={item.id}>
          {item.separatorBefore && i > 0 && <div className="context-menu-sep" />}
          <div
            className={`context-menu-item${item.danger ? " danger" : ""}${item.disabled ? " disabled" : ""}`}
            onClick={() => {
              if (item.disabled) return;
              dispatch({ type: "MENU_CLOSE" });
              item.onSelect();
            }}
          >
            {item.label}
          </div>
        </div>
      ))}
    </div>
  );
}
