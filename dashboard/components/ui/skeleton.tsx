import clsx from "clsx";
export function Skeleton({ className }: { className?: string }) {
  return (
    <div
      className={clsx(
        "animate-pulse rounded-md bg-neutral-800/60",
        className,
      )}
      aria-hidden="true"
    />
  );
}
