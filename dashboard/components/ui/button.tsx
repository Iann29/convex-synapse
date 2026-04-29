import * as React from "react";
import clsx from "clsx";

type Variant = "primary" | "secondary" | "ghost" | "danger" | "outline";
type Size = "sm" | "md";

// Variants are tuned for a near-black surface. Primary is the brand accent
// (cyan→violet shifted toward violet to match the favicon), secondary is a
// neutral fill with a 1px border, ghost is bare. Disabled states keep the
// shape but drop saturation — never collapse the layout when a button is
// pending.
const variants: Record<Variant, string> = {
  primary:
    "bg-violet-500 text-white hover:bg-violet-400 active:bg-violet-600 shadow-[0_1px_0_0_rgba(255,255,255,0.06)_inset] disabled:bg-violet-500/40 disabled:text-white/70",
  secondary:
    "bg-neutral-900 text-neutral-100 border border-neutral-800 hover:bg-neutral-800 hover:border-neutral-700 disabled:opacity-60",
  outline:
    "bg-transparent text-neutral-200 border border-neutral-800 hover:bg-neutral-900 hover:border-neutral-700 disabled:opacity-60",
  ghost:
    "bg-transparent text-neutral-300 hover:bg-neutral-900 hover:text-neutral-100 disabled:opacity-50",
  danger:
    "bg-red-600 text-white hover:bg-red-500 active:bg-red-700 disabled:bg-red-900 disabled:text-red-300",
};

const sizes: Record<Size, string> = {
  sm: "h-7 px-2.5 text-xs gap-1.5",
  md: "h-9 px-3.5 text-sm gap-2",
};

type Props = React.ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: Variant;
  size?: Size;
};

export const Button = React.forwardRef<HTMLButtonElement, Props>(
  ({ className, variant = "primary", size = "md", ...props }, ref) => (
    <button
      ref={ref}
      className={clsx(
        "inline-flex items-center justify-center rounded-md font-medium transition-colors duration-150",
        "disabled:cursor-not-allowed focus:outline-none focus-visible:ring-2 focus-visible:ring-violet-400/70 focus-visible:ring-offset-2 focus-visible:ring-offset-neutral-950",
        variants[variant],
        sizes[size],
        className
      )}
      {...props}
    />
  )
);
Button.displayName = "Button";
