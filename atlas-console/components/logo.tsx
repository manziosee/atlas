/**
 * Atlas mark: a planet carrying a tilted orbit with three nodes on it, for
 * a cluster that keeps a shared view of every service across its members.
 */
export function Logo({ className = "h-8 w-8" }: { className?: string }) {
  // Every copy defines identical gradients, so a shared ID is safe.
  const id = "atlas-logo";
  return (
    <svg viewBox="0 0 40 40" className={className} aria-hidden="true" fill="none">
      <defs>
        <linearGradient id={`${id}-g`} x1="4" y1="4" x2="36" y2="36" gradientUnits="userSpaceOnUse">
          <stop stopColor="#b6f27a" />
          <stop offset=".55" stopColor="#2fd3b5" />
          <stop offset="1" stopColor="#42a8f5" />
        </linearGradient>
        <radialGradient id={`${id}-p`} cx="14" cy="13" r="18" gradientUnits="userSpaceOnUse">
          <stop stopColor="#2fd3b5" stopOpacity=".55" />
          <stop offset="1" stopColor="#04090b" stopOpacity="0" />
        </radialGradient>
      </defs>
      <circle cx="20" cy="20" r="11" fill={`url(#${id}-p)`} stroke={`url(#${id}-g)`} strokeWidth="1.8" />
      <ellipse cx="20" cy="20" rx="18" ry="7" transform="rotate(-24 20 20)" stroke={`url(#${id}-g)`} strokeWidth="1.6" />
      <circle cx="4.6" cy="26.6" r="2.6" fill="#b6f27a" />
      <circle cx="35.4" cy="13.4" r="2.6" fill="#42a8f5" />
      <circle cx="25.6" cy="28.4" r="2.2" fill="#2fd3b5" />
    </svg>
  );
}

export function Wordmark({ className = "" }: { className?: string }) {
  return (
    <span className={`flex items-center gap-2 ${className}`}>
      <Logo className="h-7 w-7" />
      <span className="text-[1.05rem] font-semibold tracking-tight">Atlas</span>
    </span>
  );
}
