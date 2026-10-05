const {
  Cam, clamp, circ, facing, fit, mk, open, poly, prism, proj, rrect, rings,
  spring, stepS, pointer, put, register, disposer, solid,
} = HL;

const PH = 4, HOST = [-49, -34, 49, 34], DISK = [-42, -29, 42, 29], MEM = [-36, -24, 36, 24], BODY = 46;
const CLOSED = [0.8, 1.15, 1.35], SPREAD = [0.55, 0.9, 1.15], REST = 0.44, MAX = 28;

const mapRing = (P, ring, z) => ring.map((q) => P(q.u, q.v, z));
const pathTop = (P, ring, z) => poly(mapRing(P, ring, z));
const facePath = (P, ring, y, z) => poly(ring.map((q) => P(q.u, y, z + q.v)));
const cls = (el, c) => el.setAttribute("class", c);
const shift = (ring, x, y) => ring.map((q) => ({ ...q, u: q.u + x, v: q.v + y }));

function levels(t, max) {
  const g = SPREAD.map((f, i) => CLOSED[i] + f * max * t);
  const disk = g[0], memory = disk + PH + g[1], bot = memory + PH + g[2];
  return { disk, memory, bot };
}

function centerBands(P, max) {
  const z = levels(REST, max);
  const rows = [
    { name: "bot", y: P(0, 0, z.bot + BODY * 0.52)[1] },
    { name: "memory", y: P(0, 0, z.memory + PH)[1] },
    { name: "disk", y: P(0, 0, z.disk + PH)[1] },
  ].sort((a, b) => a.y - b.y);
  const top = rows[0].y - 26, bot = rows[2].y + 26;
  return { rows, cuts: [top, (rows[0].y + rows[1].y) / 2, (rows[1].y + rows[2].y) / 2, bot] };
}

function guide(P, a, b, z0, z1) {
  const pts = [[a, b], [-a, b], [a, -b], [-a, -b]];
  return pts.map(([x, y]) => `M${P(x, y, z0).join(" ")}L${P(x, y, z1).join(" ")}`).join("");
}

function mount({ stage, svg, read }, value) {
  const bag = disposer();
  let maxGap = value, active = -1, drawn = NaN, over = false;
  const C = Cam(45, 0.5, 1.94);
  const hi = levels(1, MAX);
  fit(C, [[-52, -37, -3], [52, 37, -3], [-52, 37, -3], [52, -37, -3], [0, 0, hi.bot + BODY]], 200, 174);
  const P = proj(C), front = facing(C), band = centerBands(P, 18);
  const gap = spring(REST, { eps: 0.002 });

  const g = mk("g", {}, svg);
  const host = solid(g), gd = mk("path", { class: "nf dash" }, g);
  const diskG = mk("g", {}, g), disk = solid(diskG);
  const diskGroove = mk("path", { class: "nf lo" }, diskG), diskHub = mk("path", { class: "nf" }, diskG);
  const gm = mk("path", { class: "nf dash" }, g);
  const memG = mk("g", {}, g), mem = solid(memG), chips = [];
  for (let i = 0; i < 5; i++) chips.push(mk("path", { class: "nf lo" }, memG));
  const gb = mk("path", { class: "nf dash" }, g);
  const botG = mk("g", {}, g), bot = solid(botG), eyes = [mk("path", { class: "nf" }, botG), mk("path", { class: "nf" }, botG)];

  const [hostR, hostI] = rings(...HOST, 7, 1.6);
  const [diskR, diskI] = rings(...DISK, 6, 1.2);
  const [memR, memI] = rings(...MEM, 5.5, 1.2);
  const [bodyR, bodyI] = rings(-BODY / 2, -BODY / 2, BODY / 2, BODY / 2, 8, 1.5);
  put(host, prism(P, front, hostR, hostI, -3, 0));

  function pick(p) {
    const y = p[1], c = band.cuts;
    if (y < c[0] || y > c[3]) return -1;
    return y < c[1] ? 2 : y < c[2] ? 1 : 0;
  }

  function setActive(a) {
    active = a;
    read.textContent = a < 0 ? "rest" : ["disk", "memory", "bot"][a];
    [disk.sil, mem.sil, bot.sil].forEach((el, i) => el.classList.toggle("hi", i === a));
    chips.forEach((el) => cls(el, a < 0 ? "nf hi" : "nf lo"));
  }

  function draw(t) {
    if (Math.abs(t - drawn) < 0.001) return;
    drawn = t;
    const z = levels(t, maxGap);
    put(disk, prism(P, front, diskR, diskI, z.disk, z.disk + PH));
    put(mem, prism(P, front, memR, memI, z.memory, z.memory + PH));
    put(bot, prism(P, front, bodyR, bodyI, z.bot, z.bot + BODY));
    gd.setAttribute("d", guide(P, 31, 20, 0, z.disk));
    gm.setAttribute("d", guide(P, 31, 20, z.disk + PH, z.memory));
    gb.setAttribute("d", guide(P, 21, 21, z.memory + PH, z.bot));

    const platter = shift(circ(11, 32), -21, 13), groove = shift(circ(15.5, 40), -21, 13), hub = shift(circ(3.2, 20), -21, 13), dz = z.disk + PH + 0.05;
    diskGroove.setAttribute("d", pathTop(P, groove, dz) + pathTop(P, platter, dz));
    diskHub.setAttribute("d", pathTop(P, hub, dz));

    const mz = z.memory + PH + 0.05;
    chips.forEach((el, i) => {
      const x = -22 + i * 11;
      el.setAttribute("d", pathTop(P, rrect(x, -6.5, x + 7.5, 6.5, 1.7, 5), mz));
    });

    const by = BODY / 2, bz = z.bot, ew = BODY * 0.256, eh = BODY * 0.068, cy = BODY * 0.448, er = BODY * 0.034;
    [-BODY * 0.21, BODY * 0.21].forEach((cx, i) => {
      eyes[i].setAttribute("d", facePath(P, rrect(cx - ew / 2, cy - eh / 2, cx + ew / 2, cy + eh / 2, er, 5), by, bz));
    });
  }

  const loop = register(stage, (dt) => {
    const moving = stepS(gap, dt);
    draw(gap.x);
    return moving;
  });
  bag.add(loop.unregister);

  bag.add(pointer(stage, {
    move: (p) => {
      over = true;
      gap.t = clamp((p[0] - 128) / 132, 0, 1);
      setActive(pick(p));
      loop.wake();
    },
    leave: () => { over = false; gap.t = REST; setActive(-1); loop.wake(); },
  }));

  setActive(-1);
  draw(gap.x);
  bag.add(() => svg.replaceChildren());
  return { set: (v) => { maxGap = v; drawn = NaN; if (!over) gap.t = REST; loop.wake(); }, destroy: bag.dispose };
}

hairline({
  name: "save-point",
  means: "A sleeping machine and its snapshot: memory and disk come apart beneath it, then return intact.",
  rules: [1, 3, 4, 5, 8, 9],
  range: [10, 18, 28],
  mount,
});
