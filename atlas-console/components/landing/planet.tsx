/**
 * The hero planet: an atmosphere-lit horizon with a ring of cluster nodes
 * gossiping along an orbit. Pure CSS and SVG, no images to load.
 */
export function Planet() {
  const nodes = [
    { x: 120, y: 236, c: "#b6f27a", d: "0s" },
    { x: 330, y: 150, c: "#2fd3b5", d: ".6s" },
    { x: 600, y: 112, c: "#42a8f5", d: "1.2s" },
    { x: 870, y: 150, c: "#2fd3b5", d: "1.8s" },
    { x: 1080, y: 236, c: "#b6f27a", d: "2.4s" },
  ];
  return (
    <div aria-hidden="true" className="pointer-events-none relative mx-auto -mt-6 h-[300px] w-full max-w-6xl sm:h-[380px] lg:h-[440px]">
      {/* glow behind the horizon */}
      <div className="absolute inset-x-[-10%] bottom-0 h-[85%] bg-[radial-gradient(50%_70%_at_50%_100%,rgb(47_211_181/0.35),transparent_70%)]" />
      <div className="absolute bottom-[-10%] left-[-5%] h-[70%] w-[45%] bg-[radial-gradient(closest-side,rgb(182_242_122/0.45),transparent)] blur-2xl" />
      <div className="absolute bottom-[-10%] right-[-5%] h-[70%] w-[45%] bg-[radial-gradient(closest-side,rgb(66_168_245/0.45),transparent)] blur-2xl" />

      {/* the planet */}
      <div className="absolute left-1/2 top-[38%] aspect-square w-[150%] -translate-x-1/2 overflow-hidden rounded-full sm:w-[120%] lg:w-[105%]">
        <div className="absolute inset-0 rounded-full bg-[radial-gradient(60%_45%_at_50%_8%,#1d5d63_0%,#0d2a31_38%,#06131a_70%)]" />
        <div className="animate-spin-slow absolute inset-0 rounded-full opacity-50 [background:repeating-radial-gradient(circle_at_40%_20%,transparent_0_22px,rgb(255_255_255/0.05)_23px_24px)]" />
        <div className="absolute inset-0 rounded-full shadow-[inset_0_14px_40px_rgb(182_242_122/0.55),inset_0_2px_6px_rgb(255_255_255/0.6)]" />
      </div>

      {/* gossip orbit */}
      <svg viewBox="0 0 1200 440" className="absolute inset-0 h-full w-full" preserveAspectRatio="xMidYMid slice">
        <defs>
          <linearGradient id="orbit" x1="0" x2="1">
            <stop offset="0" stopColor="#b6f27a" stopOpacity="0" />
            <stop offset=".25" stopColor="#b6f27a" stopOpacity=".7" />
            <stop offset=".5" stopColor="#2fd3b5" stopOpacity=".9" />
            <stop offset=".75" stopColor="#42a8f5" stopOpacity=".7" />
            <stop offset="1" stopColor="#42a8f5" stopOpacity="0" />
          </linearGradient>
        </defs>
        <path d="M -20 300 Q 600 -10 1220 300" stroke="url(#orbit)" strokeWidth="1.5" fill="none" />
        <path d="M -20 300 Q 600 -10 1220 300" stroke="url(#orbit)" strokeWidth="1.5" fill="none" strokeDasharray="4 10" opacity=".7">
          <animate attributeName="stroke-dashoffset" from="0" to="-140" dur="4s" repeatCount="indefinite" />
        </path>
        {nodes.map((n) => (
          <g key={n.x}>
            <circle cx={n.x} cy={n.y} r="14" fill={n.c} opacity=".15">
              <animate attributeName="r" values="10;20;10" dur="3s" begin={n.d} repeatCount="indefinite" />
              <animate attributeName="opacity" values=".25;0;.25" dur="3s" begin={n.d} repeatCount="indefinite" />
            </circle>
            <circle cx={n.x} cy={n.y} r="5" fill={n.c} />
          </g>
        ))}
      </svg>
      <div className="absolute inset-x-0 bottom-0 h-24 bg-gradient-to-t from-bg to-transparent" />
    </div>
  );
}
