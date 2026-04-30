import * as React from "react";
import clsx from "clsx";

// Card surface — sits one shade above the page background and uses a soft
// 1px border. Hoverable variants are opt-in (consumers add a className) so
// non-interactive cards stay completely still.
export function Card({
  className,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={clsx(
        "rounded-lg border border-neutral-800/80 bg-neutral-900/40 shadow-[0_0_0_1px_rgba(255,255,255,0.01)_inset]",
        className
      )}
      {...props}
    />
  );
}

export function CardHeader({
  className,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={clsx(
        "border-b border-neutral-800/80 px-5 py-3.5",
        className,
      )}
      {...props}
    />
  );
}

export function CardBody({
  className,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) {
  return <div className={clsx("p-5", className)} {...props} />;
}

export function CardTitle({
  className,
  ...props
}: React.HTMLAttributes<HTMLHeadingElement>) {
  return (
    <h3
      className={clsx("text-sm font-semibold text-neutral-100", className)}
      {...props}
    />
  );
}

export function CardDescription({
  className,
  ...props
}: React.HTMLAttributes<HTMLParagraphElement>) {
  return (
    <p
      className={clsx("mt-1 text-xs text-neutral-400", className)}
      {...props}
    />
  );
}
