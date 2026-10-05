/**
 * Dormitory: a low host slab carrying nine sleeping machines. The pointer is
 * projected to the ground plane; each bot wakes by distance to it, on its own
 * spring. Wakefulness lifts the body and opens its eyes. One bot is bright at
 * rest; while pointing, the nearest bot takes the bright stroke.
 */
const {
  Cam, clamp, facing, fit, lerp, poly, prism, proj, rings, rrect, spring, stepS,
  unproj, disposer, mk, pointer, put, register, solid,
} = HL;

const L = 30, GX = 42, GY = 34, ZSTEP = 15, COLS = 3, ROWS = 3, HOST_Z = -6;
const HOST = [-13, -11, (COLS - 1) * GX + L + 13, (ROWS - 1) * GY + L + 11];
const NAMES = ["app", "api", "web", "db", "ci", "docs", "bot", "lab", "edge"];
const REST = [0.02, 0.10, 0.04, 0.12, 1, 0.06, 0.15, 0.00, 0.08];
const REST_I = 4, FLOOR = 0.07;

const falloff = (u) =>
  u >= 1 ? FLOOR : 0.16 + 0.84 * (1 - u) ** 0.82;

function eyePath(P, x, y, z, wake, left) {
  const cx = x + (left ? lerp(0.29, 0.32, wake) : lerp(0.71, 0.68, wake)) * L;
  const cz = lerp(0.448, 0.50, wake) * L;
  const ew = lerp(0.256, 0.13, wake) * L, eh = lerp(0.068, 0.15, wake) * L;
  const er = Math.min(0.034 * L, ew / 2, eh / 2);
  return poly(rrect(cx - ew / 2, cz - eh / 2, cx + ew / 2, cz + eh / 2, er, 7).map((q) => P(q.u, y + L, z + q.v)));
}

function mount({ stage, svg, read }, value) {
  const bag = disposer();
  const C = Cam(45, 0.5, 1.62);
  fit(C, [
    [HOST[0], HOST[1], HOST_Z], [HOST[2], HOST[3], HOST_Z],
    [HOST[2], HOST[1], HOST_Z], [HOST[0], HOST[3], HOST_Z],
    [HOST[0], HOST[1], ROWS * ZSTEP + L], [HOST[2], HOST[3], ROWS * ZSTEP + L],
  ], 200, 166);
  const P = proj(C), front = facing(C);
  let radius = value, over = null, nearest = null;

  const g = mk("g", {}, svg);
  for (let j = 0; j < ROWS; j++) {
    const top = (ROWS - 1 - j) * ZSTEP, y = j * GY;
    const [shelf, shelfIn] = rings(HOST[0], y - 8, HOST[2], y + L + 8, 12, 2);
    put(solid(g), prism(P, front, shelf, shelfIn, HOST_Z, top));
  }

  const bots = [];
  for (let s = 0; s <= COLS + ROWS - 2; s++) for (let i = 0; i < COLS; i++) {
    const j = s - i;
    if (j < 0 || j >= ROWS) continue;
    const k = j * COLS + i, x = i * GX, y = j * GY, z = (ROWS - 1 - j) * ZSTEP;
    const grp = mk("g", {}, g), body = solid(grp);
    const eyes = [mk("path", { class: "nf" }, grp), mk("path", { class: "nf" }, grp)];
    const [ring, inner] = rings(x, y, x + L, y + L, 5.4, 1.15);
    bots.push({ i, j, k, x, y, z, cx: x + L / 2, cy: y + L / 2, name: NAMES[k], ring, inner, body, eyes, sp: spring(REST[k], { eps: 0.003 }), drawn: -1 });
  }

  function pick(p) {
    let best = bots[0], bd = Infinity;
    for (const b of bots) {
      const h = lerp(0.60 * L, L, REST[b.k]), q = unproj(C, p[0], p[1], b.z + h);
      const inside = q[0] >= b.x - 4 && q[0] <= b.x + L + 4 && q[1] >= b.y - 4 && q[1] <= b.y + L + 4;
      const c = P(b.cx, b.cy + L, b.z + h * 0.50);
      const d = inside ? 0 : (c[0] - p[0]) ** 2 + (c[1] - p[1]) ** 2;
      if (d < bd) { bd = d; best = b; }
    }
    return best;
  }

  function retarget() {
    nearest = over && pick(lastPoint);
    for (const b of bots) {
      if (!over) { b.sp.t = REST[b.k]; continue; }
      const h0 = lerp(0.60 * L, L, REST[b.k]);
      const q = unproj(C, lastPoint[0], lastPoint[1], b.z + h0);
      const planeD = Math.hypot((b.cx - q[0]) / GX, (b.cy - q[1]) / GY);
      const cellD = nearest ? Math.hypot(b.i - nearest.i, b.j - nearest.j) : planeD;
      const d = Math.min(planeD, cellD);
      b.sp.t = b === nearest ? 1 : falloff(d / radius);
    }
    read.textContent = nearest ? nearest.name : "rest";
    B.wake();
  }

  function draw(b, hi) {
    const wake = clamp(b.sp.x, 0, 1);
    if (Math.abs(wake - b.drawn) < 0.001 && b.hi === hi) return;
    b.drawn = wake; b.hi = hi;
    const h = lerp(0.60 * L, L, wake);
    put(b.body, prism(P, front, b.ring, b.inner, b.z, b.z + h));
    b.body.sil.classList.toggle("hi", hi);
    b.eyes[0].setAttribute("d", eyePath(P, b.x, b.y, b.z, wake, true));
    b.eyes[1].setAttribute("d", eyePath(P, b.x, b.y, b.z, wake, false));
    b.eyes.forEach((e) => e.setAttribute("class", "nf" + (hi ? " hi" : "")));
  }

  const B = register(stage, (dt) => {
    let moving = false;
    for (const b of bots) {
      if (stepS(b.sp, dt)) moving = true;
      draw(b, nearest ? b === nearest : b.k === REST_I);
    }
    return moving;
  });
  bag.add(B.unregister);

  let lastPoint = null;
  bag.add(pointer(stage, {
    move: (p) => { lastPoint = p; over = unproj(C, p[0], p[1], 0); retarget(); },
    leave: () => { over = null; retarget(); },
  }));
  bag.add(() => svg.replaceChildren());

  return {
    set: (v) => { radius = v; if (over) retarget(); },
    destroy: bag.dispose,
  };
}

hairline({
  name: "dormitory",
  means: "Sleeping machines on one host: the ones near the pointer wake, rise and open their eyes.",
  rules: [1, 3, 4, 5, 8, 9],
  range: [0.8, 1.6, 2.8],
  mount,
});
