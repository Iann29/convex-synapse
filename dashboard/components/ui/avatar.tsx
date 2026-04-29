import clsx from "clsx";
import * as React from "react";

// Deterministic gradient avatar.
//
// Same `seed` always produces the same hue pair + initials, so a user's
// avatar is stable across sessions and across the codebase. We use FNV-1a
// (32-bit) which is small, fast, and good enough for picking two hues
// 60 degrees apart on the HSL wheel.
//
// We deliberately avoid randomness or any backend round-trip: callers pass
// the seed (email for users, slug for teams) and the rendering is pure.

type Size = "xs" | "sm" | "md" | "lg";

const sizeClasses: Record<Size, string> = {
  xs: "h-5 w-5 text-[9px]",
  sm: "h-6 w-6 text-[10px]",
  md: "h-7 w-7 text-[11px]",
  lg: "h-10 w-10 text-sm",
};

function fnv1a(input: string): number {
  let hash = 0x811c9dc5;
  for (let i = 0; i < input.length; i++) {
    hash ^= input.charCodeAt(i);
    // Multiply by FNV prime, mod 2^32.
    hash = (hash + ((hash << 1) + (hash << 4) + (hash << 7) + (hash << 8) + (hash << 24))) >>> 0;
  }
  return hash;
}

function initialsFor(seed: string, label?: string): string {
  // Prefer caller-supplied label (e.g. team name): pick first char of the
  // first two non-empty words. Falls back to first two letters of the seed.
  const source = (label ?? seed).trim();
  if (!source) return "??";
  const parts = source.split(/[\s._\-@]+/).filter(Boolean);
  if (parts.length >= 2) {
    return (parts[0][0] + parts[1][0]).toUpperCase();
  }
  // Single-word: take the first two characters.
  return source.slice(0, 2).toUpperCase();
}

type Props = {
  seed: string;
  // Display label — used for initials and the title attribute. Falls
  // back to the seed.
  label?: string;
  size?: Size;
  className?: string;
} & Omit<React.HTMLAttributes<HTMLDivElement>, "children">;

export function Avatar({ seed, label, size = "md", className, ...rest }: Props) {
  const hash = fnv1a(seed || "?");
  const hue = hash % 360;
  // Two stops, second hue offset by 56 degrees so the gradient reads as a
  // distinct sweep and not a near-monochrome blur. Saturation/lightness
  // tuned to sit comfortably on the near-black surface.
  const a = `hsl(${hue} 70% 55%)`;
  const b = `hsl(${(hue + 56) % 360} 70% 40%)`;
  const initials = initialsFor(seed, label);

  return (
    <div
      {...rest}
      title={label ?? seed}
      aria-hidden={rest["aria-label"] ? undefined : true}
      className={clsx(
        "inline-flex shrink-0 items-center justify-center rounded-full font-semibold tracking-tight text-white/95 ring-1 ring-white/10 select-none",
        sizeClasses[size],
        className,
      )}
      style={{ backgroundImage: `linear-gradient(135deg, ${a}, ${b})` }}
    >
      {initials}
    </div>
  );
}
