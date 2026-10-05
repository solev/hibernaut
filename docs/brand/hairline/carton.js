const {
  Cam, clamp, facing, fit, hull, lerp, open, poly, prism, proj, rad, ringAt, rings, rrect, run,
  disposer, mk, place, pointer, put, register, solid, spring, stepS,
} = HL;

const X0 = -8, Y0 = -8, X1 = 84, Y1 = 96, Z0 = -13, TOP = 12;
const WALL = 5, L = 24, COLS = 2, ROWS = 3, GX = 39, GY = 28, BOT_Z = -5;
const REST = 26, THICK = 3, LID_D = Y1 - Y0;
const ROW_WAKE = [72, 54, 34];
const botX = (i) => 8 + i * GX, botY = (j) => 9 + j * GY;
const smooth = (t) => t * t * (3 - 2 * t);
const LR = (pts) => (pts[0][0] <= pts[pts.length - 1][0] ? pts : pts.slice().reverse());

function eye(P, b, w, left) {
  const cx = b.x + (left ? lerp(0.29, 0.32, w) : lerp(0.71, 0.68, w)) * L;
  const cz = b.z + lerp(0.448, 0.50, w) * L;
  const ew = lerp(0.256, 0.13, w) * L, eh = lerp(0.068, 0.15, w) * L;
  const er = Math.min(0.034 * L, ew / 2, eh / 2);
  return poly(rrect(cx - ew / 2, cz - eh / 2, cx + ew / 2, cz + eh / 2, er, 7).map((q) => P(q.u, b.y + L, q.v)));
}

function lidPath(P, a, dz, keep) {
  const ca = Math.cos(rad(a)), sa = Math.sin(rad(a));
  const ring = run(rrect(X0 + 1, 0, X1 - 1, LID_D, 10, 12), keep || (() => true));
  const pts = ring.map((q) => P(q.u, Y0 + q.v * ca, TOP + 1 + q.v * sa + dz));
  return keep ? open(pts) : poly(pts);
}

function frontWall(P, front, outer, inner) {
  const iF = LR(ringAt(P, run(inner, front), TOP));
  const oT = LR(ringAt(P, run(outer, front), TOP));
  const oB = LR(ringAt(P, run(outer, front), Z0));
  return [
    [poly([...iF, oT[oT.length - 1], ...oB.slice().reverse(), oT[0]]), ""],
    [open(oT), "nf lo"], [open(iF), "nf"], [open([oT[0], ...oB, oT[oT.length - 1]]), "nf sil"],
  ];
}

function mount({ stage, svg, read }, value) {
  const bag = disposer();
  let maxOpen = value, over = false, lastPoint = null;
  const C = Cam(45, 0.5, 1.70);
  fit(C, [
    [X0 - 4, Y0, Z0], [X1 + 4, Y1 + 2, Z0],
    [X0, Y0 + LID_D * Math.cos(rad(REST)), TOP + LID_D * Math.sin(rad(REST))],
    [X1, Y0 + LID_D * Math.cos(rad(REST)), TOP + LID_D * Math.sin(rad(REST))],
    [X0, Y0 + LID_D * Math.cos(rad(120)), TOP + LID_D * Math.sin(rad(120))],
    [X1, Y0 + LID_D * Math.cos(rad(120)), TOP + LID_D * Math.sin(rad(120))],
  ], 204, 166);
  const P = proj(C), front = facing(C);
  const g = mk("g", {}, svg), back = mk("g", {}, g), lidG = mk("g", {}, g), botsG = mk("g", {}, g), frontG = mk("g", {}, g);
  const [outer, inner] = rings(X0, Y0, X1, Y1, 13, WALL);
  put(solid(back), prism(P, front, outer, inner, Z0, TOP));
  for (let j = 0; j < ROWS; j++) for (let i = 0; i < COLS; i++) {
    const bx = botX(i), by = botY(j);
    mk("path", { d: poly(ringAt(P, rrect(bx - 3, by - 3, bx + L + 3, by + L + 3, 8, 8), TOP + 0.25)), class: "nf lo" }, back);
  }
  mk("path", { d: open([P(X0 + 13, Y0, TOP + 2), P(X1 - 13, Y0, TOP + 2)]), class: "nf lo" }, back);

  const lidFace = mk("path", { class: "sil" }, lidG);
  const lidThick = mk("path", { class: "nf lo" }, lidG);
  const lidHinge = mk("path", { class: "nf" }, lidG);

  const bots = [];
  for (let s = 0; s <= COLS + ROWS - 2; s++) for (let i = 0; i < COLS; i++) {
    const j = s - i;
    if (j < 0 || j >= ROWS) continue;
    const x = botX(i), y = botY(j), grp = mk("g", {}, botsG), body = solid(grp);
    const eyes = [mk("path", { class: "nf" }, grp), mk("path", { class: "nf" }, grp)];
    const [ring, innerB] = rings(x, y, x + L, y + L, 5.2, 1.1);
    bots.push({ i, j, x, y, ring, inner: innerB, body, eyes, sp: spring(0, { eps: 0.02 }), z: BOT_Z, drawn: "" });
  }

  for (const [d, cls] of frontWall(P, front, outer, inner)) mk("path", { d, class: cls }, frontG);
  const dot = mk("circle", { r: 1.9, class: "dot" }, frontG);
  place(dot, P((X0 + X1) / 2, Y1 + 0.4, 1.6));

  const lid = spring(REST, { eps: 0.02 });
  function angleFor(p) { return clamp((194 - p[1]) / 112, 0, 1) * maxOpen; }
  function stateName(a) { return a < 8 ? "off" : a > maxOpen * 0.55 ? "up" : "waking"; }
  function retarget(p) {
    over = !!p; lastPoint = p || lastPoint;
    lid.t = p ? angleFor(p) : REST;
    read.textContent = p ? stateName(lid.t) : "rest";
    B.wake();
  }

  function wakeTargets(a) {
    for (const b of bots) {
      const rowFromFront = ROWS - 1 - b.j;
      b.sp.t = smooth(clamp((a - ROW_WAKE[b.j]) / 22, 0, 1)) * (1 - rowFromFront * 0.04);
    }
  }

  function drawBot(b) {
    const w = clamp(b.sp.x, 0, 1), rise = 5.6 * w, h = lerp(0.48 * L, 0.84 * L, w), z = BOT_Z + rise;
    const key = w.toFixed(3);
    if (key === b.drawn) return;
    b.drawn = key; b.z = z;
    put(b.body, prism(P, front, b.ring, b.inner, z, z + h));
    b.eyes[0].setAttribute("d", eye(P, b, w, true));
    b.eyes[1].setAttribute("d", eye(P, b, w, false));
  }

  function draw() {
    const a = clamp(lid.x, 0, maxOpen), activeLid = over && lid.t > maxOpen * 0.55;
    lidFace.setAttribute("d", lidPath(P, a, 0));
    lidThick.setAttribute("d", lidPath(P, a, -THICK, (q) => q.v > LID_D - 13 || q.u < X0 + 5 || q.u > X1 - 5));
    lidHinge.setAttribute("d", open([P(X0 + 12, Y0, TOP + 1), P(X1 - 12, Y0, TOP + 1)]));
    lidFace.setAttribute("class", "sil" + (activeLid ? " hi" : ""));
    if (a > 44) back.after(lidG); else frontG.after(lidG);
    const dotClass = !over ? "dot" : lid.t < 8 ? "dot off" : activeLid ? "dot m" : "dot";
    dot.setAttribute("class", dotClass);
    for (const b of bots) drawBot(b);
  }

  const B = register(stage, (dt) => {
    let moving = stepS(lid, dt);
    wakeTargets(lid.x);
    for (const b of bots) if (stepS(b.sp, dt)) moving = true;
    draw();
    return moving;
  });
  bag.add(B.unregister);

  wakeTargets(lid.x); draw();
  bag.add(pointer(stage, { move: retarget, leave: () => retarget(null) }));
  bag.add(() => svg.replaceChildren());
  return { set: (v) => { maxOpen = v; if (over) retarget(lastPoint); }, destroy: bag.dispose };
}

hairline({
  name: "carton",
  means: "The host closes when idle; lift the lid and the machines inside wake where they left off.",
  rules: [1, 3, 4, 5, 8, 9],
  range: [60, 95, 120],
  mount,
});
