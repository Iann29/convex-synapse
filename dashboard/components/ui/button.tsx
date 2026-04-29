import * as React from "react";
import clsx from "clsx";

type Variant = "primary" | "secondary" | "ghost" | "danger";
type Size = "sm" | "md";

const variants: Record<Variant, string> = {
  primary:
    "bg-white text-neutral-950 hover:bg-neutral-200 disabled:bg-neutral-700 disabled:text-neutral-400",
  secondary:
    "bg-neutral-800 text-neutral-100 hover:bg-neutral-700 border border-neutral-700",
  ghost:
    "bg-transparent text-neutral-300 hover:bg-neutral-800 hover:text-neutral-100",
  danger:
    "bg-red-600 text-white hover:bg-red-500 disabled:bg-red-900 disabled:text-red-300",
};

const sizes: Record<Size, string> = {
  sm: "h-8 px-3 text-xs",
  md: "h-9 px-4 text-sm",
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
        "inline-flex items-center justify-center rounded-md font-medium transition-colors",
        "disabled:cursor-not-allowed focus:outline-none focus-visible:ring-2 focus-visible:ring-neutral-400",
        variants[variant],
        sizes[size],
        className
      )}
      {...props}
    />
  )
);
Button.displayName = "Button";
