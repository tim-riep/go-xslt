// A small, hand-drawn inline-SVG icon set — just the handful of glyphs this
// app actually needs, so there's no icon-library dependency to pull in.
// Every path uses currentColor, so an icon tints via ordinary CSS `color`.
import type { CSSProperties } from "react";

export type IconName =
  | "chevron-right"
  | "close"
  | "plus"
  | "play"
  | "trash"
  | "pencil"
  | "duplicate"
  | "refresh"
  | "more"
  | "warning"
  | "check"
  | "folder"
  | "folder-plus"
  | "file-plus";

export function Icon({
  name,
  size = 14,
  className,
  style,
}: {
  name: IconName;
  size?: number;
  className?: string;
  style?: CSSProperties;
}) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 16 16"
      fill="none"
      aria-hidden="true"
      className={className}
      style={{ display: "block", flexShrink: 0, ...style }}
    >
      {renderIcon(name)}
    </svg>
  );
}

function renderIcon(name: IconName) {
  switch (name) {
    case "chevron-right":
      return <path d="M6 4l4 4-4 4" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />;
    case "close":
      return <path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />;
    case "plus":
      return <path d="M8 3v10M3 8h10" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />;
    case "play":
      return <path d="M5 3.3v9.4l7.5-4.7z" fill="currentColor" />;
    case "trash":
      return (
        <path
          d="M3 4.5h10M6.5 4.5V3a1 1 0 0 1 1-1h1a1 1 0 0 1 1 1v1.5M5 4.5v8.5a1 1 0 0 0 1 1h4a1 1 0 0 0 1-1V4.5"
          stroke="currentColor"
          strokeWidth="1.3"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
      );
    case "pencil":
      return (
        <path
          d="M10.5 2.5l3 3-7.6 7.6-3.4.7.7-3.4z"
          stroke="currentColor"
          strokeWidth="1.2"
          strokeLinejoin="round"
        />
      );
    case "duplicate":
      return (
        <>
          <rect x="5.5" y="5.5" width="7" height="8" rx="1" stroke="currentColor" strokeWidth="1.2" />
          <path d="M3.5 10.5v-7a1 1 0 0 1 1-1h7" stroke="currentColor" strokeWidth="1.2" />
        </>
      );
    case "refresh":
      return (
        <path
          d="M12.8 5.2A5 5 0 1 0 13.5 9M13 3v3h-3"
          stroke="currentColor"
          strokeWidth="1.3"
          strokeLinecap="round"
          strokeLinejoin="round"
          fill="none"
        />
      );
    case "more":
      return (
        <>
          <circle cx="4" cy="8" r="1.2" fill="currentColor" />
          <circle cx="8" cy="8" r="1.2" fill="currentColor" />
          <circle cx="12" cy="8" r="1.2" fill="currentColor" />
        </>
      );
    case "warning":
      return (
        <path
          d="M8 2.5l6.2 10.7a1 1 0 0 1-.87 1.5H2.67a1 1 0 0 1-.87-1.5L8 2.5ZM8 6.5v3.2M8 11.7v.1"
          stroke="currentColor"
          strokeWidth="1.3"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
      );
    case "check":
      return <path d="M3.5 8.5l3 3 6-7" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />;
    case "folder":
      return <path d="M1.5 4h4l1 1.3h8v7.7a.7.7 0 0 1-.7.7H2.2a.7.7 0 0 1-.7-.7V4Z" fill="currentColor" />;
    case "folder-plus":
      return (
        <>
          <path d="M1.5 4h4l1 1.3h8v7.7a.7.7 0 0 1-.7.7H2.2a.7.7 0 0 1-.7-.7V4Z" stroke="currentColor" strokeWidth="1.1" strokeLinejoin="round" />
          <path d="M8 7.8v3.4M6.3 9.5h3.4" stroke="currentColor" strokeWidth="1.2" strokeLinecap="round" />
        </>
      );
    case "file-plus":
      return (
        <>
          <path d="M4 1.8h5l3 3v8.7a.7.7 0 0 1-.7.7H4.7a.7.7 0 0 1-.7-.7V2.5a.7.7 0 0 1 .7-.7Z" stroke="currentColor" strokeWidth="1.1" strokeLinejoin="round" />
          <path d="M8 7.3v3.4M6.3 9h3.4" stroke="currentColor" strokeWidth="1.2" strokeLinecap="round" />
        </>
      );
    default:
      return null;
  }
}
