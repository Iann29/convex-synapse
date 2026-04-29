import * as React from "react";
import clsx from "clsx";

type Props = React.InputHTMLAttributes<HTMLInputElement>;

export const Input = React.forwardRef<HTMLInputElement, Props>(
  ({ className, ...props }, ref) => (
    <input
      ref={ref}
      className={clsx(
        "flex h-9 w-full rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1 text-sm text-neutral-100",
        "placeholder:text-neutral-500 focus:outline-none focus-visible:ring-2 focus-visible:ring-neutral-500",
        "disabled:cursor-not-allowed disabled:opacity-50",
        className
      )}
      {...props}
    />
  )
);
Input.displayName = "Input";
