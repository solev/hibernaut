const {
  Cam, clamp, facing, fit, hull, lerp, open, poly, prism, proj, ringAt, rings, rrect, run,
  tdone, tset, tval, tween, disposer, mk, place, pointer, put, register, solid,
} = HL;

const PL = { x0: -116, y0: -52, x1: 112, y1: 56, z0: -6, z1: 0 };
const RAIL = { x0: -58, y0: -40, x1: 100, y1: -24, z0: 0, z1: 11 };
const SOCK = { x0: -112, y0: -50, x1: -56, y1: -15, z0: 0, z1: 28 };
const BOTN = 5, B = 25, BG = 10, BY0 = 4, BH = 25, BX0 = -54;
const PORTS = [
  { x: -101, y: -32, name: "github" },
  { x: -84, y: -32, name: "codex" },
  { x: -67, y: -32, name: "copilot" },
];
const DOTS = [-4.2, 0, 4.2], LANE = 10.2;

function tileCode(parent, n, pts, r, onTop) {
  const ds = [];
  for (let k = 0; k < 3; k++) ds.push(mk("circle", { r, class: k <= n ? "dot m" : "dot off" }, parent));
  ds.forEach((d, k) => place(d, pts(k, onTop)));
  return ds;
}

function drawCode(dots, n, pts, onTop) {
  dots.forEach((d, k) => { d.setAttribute("class", k <= n ? "dot m" : "dot off"); place(d, pts(k, onTop)); });
}

function eyePath(P, x0, y1, z0, L, cx, cy, w, h, r) {
  const rr = rrect(cx - w / 2, cy - h / 2, cx + w / 2, cy + h / 2, r, 10);
  return poly(rr.map((q) => P(x0 + q.u, y1 + 0.35, z0 + q.v)));
}

function botBody(P, front, x0, y0) {
  const [ring, inner] = rings(x0, y0, x0 + B, y0 + B, 5, 1.1);
  return prism(P, front, ring, inner, 0, BH);
}

function mount({ stage, svg, read }, value) {
  const bag = disposer();
  let step = value, rest = 0, active = 0;
  const C = Cam(45, 0.5, 1.55);
  fit(C, [[PL.x0, PL.y0, PL.z0], [PL.x1, PL.y1, PL.z0], [PL.x1, PL.y0, 0], [PL.x0, PL.y1, 0], [SOCK.x0, SOCK.y0, SOCK.z1 + 18], [PL.x1, PL.y1, BH + 10]], 200, 166);
  const P = proj(C), front = facing(C);
  const g = mk("g", {}, svg);
  put(solid(g), prism(P, front, ...rings(PL.x0, PL.y0, PL.x1, PL.y1, 13, 2.2), PL.z0, PL.z1));
  put(solid(g), prism(P, front, ...rings(RAIL.x0, RAIL.y0, RAIL.x1, RAIL.y1, 6, 1.2), RAIL.z0, RAIL.z1));
  const sock = solid(g);
  put(sock, prism(P, front, ...rings(SOCK.x0, SOCK.y0, SOCK.x1, SOCK.y1, 6, 1.4), SOCK.z0, SOCK.z1));

  const ports = PORTS.map((p, i) => {
    const grp = mk("g", {}, g);
    const recess = mk("path", { class: "nf lo" }, grp);
    const ring = rrect(p.x - 5.6, p.y - 7, p.x + 5.6, p.y + 7, 3.4, 8);
    recess.setAttribute("d", poly(ringAt(P, ring, SOCK.z1 + 0.25)));
    const dots = tileCode(grp, i, (k) => P(p.x + DOTS[k], p.y - 13, SOCK.z1 + 0.7), 1.2);
    return { ...p, recess, dots, c: P(p.x, p.y, SOCK.z1 + 1) };
  });

  const plugStem = solid(g), plugHead = solid(g), plugPick = tween(rest);
  let plugFrom = rest, plugTo = rest;
  const bots = [];
  for (let i = 0; i < BOTN; i++) {
    const x0 = BX0 + i * (B + BG), y0 = BY0;
    const grp = mk("g", {}, g), body = solid(grp), tile = mk("path", { class: "nf lo" }, grp);
    const eyes = [mk("path", { class: "nf" }, grp), mk("path", { class: "nf" }, grp)];
    const lane = (rest - 1) * LANE;
    const dots = tileCode(grp, rest, (k) => P(x0 + B / 2 + lane + DOTS[k], y0 + B / 2 + 3.6, BH + 1.1), 1.1);
    bots.push({ i, x0, y0, grp, body, tile, eyes, dots, pick: tween(rest), from: rest, to: rest, drawn: "" });
  }

  function drawPlug(now) {
    const v = clamp(tval(plugPick, now), 0, 2), a = Math.floor(v), b = clamp(a + 1, 0, 2), u = v - a;
    const p = PORTS[b] ? { x: lerp(PORTS[a].x, PORTS[b].x, u), y: lerp(PORTS[a].y, PORTS[b].y, u) } : PORTS[2];
    const dist = Math.max(1, Math.abs(plugTo - plugFrom));
    const q = plugTo === plugFrom ? 1 : clamp(Math.abs(v - plugFrom) / dist, 0, 1);
    const z0 = SOCK.z1 + 0.5 + Math.sin(Math.PI * q) * 10;
    put(plugStem, prism(P, front, ...rings(p.x - 3.7, p.y - 5, p.x + 3.7, p.y + 5, 2.5, 0.7), z0, z0 + 10));
    put(plugHead, prism(P, front, ...rings(p.x - 6.5, p.y - 7.8, p.x + 6.5, p.y + 7.8, 3.8, 0.8), z0 + 7.2, z0 + 12.4));
    plugStem.sil.setAttribute("class", "sil hi"); plugHead.sil.setAttribute("class", "sil hi");
  }

  function drawBot(bt, now) {
    const v = clamp(tval(bt.pick, now), 0, 2), dist = Math.max(1, Math.abs(bt.to - bt.from));
    const q = bt.to === bt.from ? 1 : clamp(Math.abs(v - bt.from) / dist, 0, 1), code = q > 0.52 ? bt.to : bt.from;
    const z = BH + 0.7 + Math.sin(Math.PI * q) * 10, key = [v.toFixed(2), code, q.toFixed(2)].join("/");
    if (key === bt.drawn) return;
    bt.drawn = key;
    put(bt.body, botBody(P, front, bt.x0, bt.y0));
    const lane = (code - 1) * LANE;
    const tr = rrect(bt.x0 + 6.5 + lane, bt.y0 + 6.7, bt.x0 + B - 6.5 + lane, bt.y0 + B - 4.9, 3.1, 8);
    bt.tile.setAttribute("d", poly(ringAt(P, tr, z)));
    drawCode(bt.dots, code, (k) => P(bt.x0 + B / 2 + lane + DOTS[k], bt.y0 + B / 2 + 3.6, z + 0.2));
    const blink = Math.max(0, 1 - Math.abs(q - 0.72) / 0.16), w = 1 - blink * 0.96;
    const ew = lerp(0.256, 0.13, w) * B, eh = lerp(0.068, 0.15, w) * B, er = Math.min(0.034 * B, ew / 2, eh / 2);
    const cy = lerp(0.448, 0.50, w) * B;
    bt.eyes[0].setAttribute("d", eyePath(P, bt.x0, bt.y0 + B, 0, B, lerp(0.29, 0.32, w) * B, cy, ew, eh, er));
    bt.eyes[1].setAttribute("d", eyePath(P, bt.x0, bt.y0 + B, 0, B, lerp(0.71, 0.68, w) * B, cy, ew, eh, er));
  }

  const loop = register(stage, (_dt, now) => {
    let moving = !tdone(plugPick, now);
    drawPlug(now);
    bots.forEach((bt) => { drawBot(bt, now); if (!tdone(bt.pick, now)) moving = true; });
    return moving;
  });
  bag.add(loop.unregister);

  function choose(a, label) {
    if (a === active && read.textContent === label) return;
    const now = performance.now(), from = active;
    active = a; read.textContent = label;
    plugFrom = from; plugTo = a; tset(plugPick, a, now, 0);
    bots.forEach((bt) => { bt.from = from; bt.to = a; tset(bt.pick, a, now, bt.i * step); });
    loop.wake();
  }

  function hit(p) {
    let best = -1, bd = 1e9;
    ports.forEach((q, i) => {
      const dx = Math.abs(p[0] - q.c[0]), dy = Math.abs(p[1] - q.c[1]), d = dx + dy * 1.4;
      if (dx < 24 && dy < 19 && d < bd) { best = i; bd = d; }
    });
    return best;
  }

  drawPlug(performance.now()); bots.forEach((bt) => drawBot(bt, performance.now())); read.textContent = "rest";
  bag.add(pointer(stage, { move: (p) => { const h = hit(p); if (h >= 0) choose(h, PORTS[h].name); }, leave: () => choose(rest, "rest") }));
  bag.add(() => svg.replaceChildren());
  return { set: (v) => { step = v; }, destroy: bag.dispose };
}

hairline({
  name: "rail",
  means: "Connect a tool once on the host: every machine gets a placeholder; the real key never leaves the socket.",
  rules: [1, 2, 4, 8, 10],
  range: [0, 50, 110],
  mount,
});
