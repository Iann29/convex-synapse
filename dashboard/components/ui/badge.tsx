import clsx from "clsx";
import * as React from "react";

type Tone = "green" | "yellow" | "amber" | "red" | "neutral";

const tones: Record<Tone, string> = {
  green: "bg-green-500/15 text-green-400 border-green-500/30",
  yellow: "bg-yellow-500/15 text-yellow-400 border-yellow-500/30",
  // Distinct from yellow so the kind=aster preview badge reads as
  // "experimental capability" rather than "warning".
  amber: "bg-amber-500/15 text-amber-400 border-amber-500/30",
  red: "bg-red-500/15 text-red-400 border-red-500/30",
  neutral: "bg-neutral-700/40 text-neutral-300 border-neutral-700",
};

type Props = React.HTMLAttributes<HTMLSpanElement> & { tone?: Tone };

export function Badge({ className, tone = "neutral", ...props }: Props) {
  return (
    <span
      className={clsx(
        "inline-flex items-center rounded border px-2 py-0.5 text-[11px] font-medium uppercase tracking-wide",
        tones[tone],
        className
      )}
      {...props}
    />
  );
}
