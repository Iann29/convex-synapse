"use client";

import * as React from "react";
import clsx from "clsx";

type Props = {
  open: boolean;
  onClose: () => void;
  title?: string;
  children: React.ReactNode;
  className?: string;
};

export function Dialog({ open, onClose, title, children, className }: Props) {
  React.useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onClose]);

  if (!open) return null;

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm"
      onClick={onClose}
    >
      <div
        className={clsx(
          "w-full max-w-md rounded-lg border border-neutral-800 bg-neutral-950 p-5 shadow-xl",
          className
        )}
        onClick={(e) => e.stopPropagation()}
      >
        {title && (
          <h2 className="mb-4 text-base font-semibold text-neutral-100">{title}</h2>
        )}
        {children}
      </div>
    </div>
  );
}
