import clsx from "clsx";

// Synapse mark — three nodes connected through a central hub, evoking a
// neural-network synapse. Hand-rolled SVG, 24x24 viewBox, ~700 bytes
// rendered. We pick a cyan→violet gradient so we don't mimic the Convex
// Cloud orange/yellow logo (intentional hue distance to avoid trade-mark
// ambiguity per the design brief).

type Props = {
  className?: string;
  // When true, drops the gradient and uses currentColor — handy on hover
  // chips, monochrome menus, etc.
  monochrome?: boolean;
};

export function SynapseLogo({ className, monochrome = false }: Props) {
  const id = "synapse-logo-grad";
  return (
    <svg
      viewBox="0 0 24 24"
      className={clsx("h-6 w-6", className)}
      fill="none"
      role="img"
      aria-label="Synapse"
      xmlns="http://www.w3.org/2000/svg"
    >
      {!monochrome && (
        <defs>
          <linearGradient id={id} x1="0" y1="0" x2="24" y2="24" gradientUnits="userSpaceOnUse">
            <stop offset="0" stopColor="#22d3ee" />
            <stop offset="1" stopColor="#a855f7" />
          </linearGradient>
        </defs>
      )}
      {/* Connecting strokes — drawn first so the nodes sit on top. */}
      <g
        stroke={monochrome ? "currentColor" : `url(#${id})`}
        strokeWidth="1.6"
        strokeLinecap="round"
      >
        <line x1="5" y1="6" x2="12" y2="12" />
        <line x1="19" y1="6" x2="12" y2="12" />
        <line x1="12" y1="20" x2="12" y2="12" />
      </g>
      {/* Outer nodes. */}
      <g fill={monochrome ? "currentColor" : `url(#${id})`}>
        <circle cx="5" cy="6" r="2.2" />
        <circle cx="19" cy="6" r="2.2" />
        <circle cx="12" cy="20" r="2.2" />
      </g>
      {/* Hub node — outlined ring so the centre reads as a switching point. */}
      <circle
        cx="12"
        cy="12"
        r="2.6"
        fill={monochrome ? "currentColor" : "#0a0a0a"}
        stroke={monochrome ? "currentColor" : `url(#${id})`}
        strokeWidth="1.6"
      />
    </svg>
  );
}
