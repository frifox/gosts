// Combine Models: models (made ready in the model editor) and simple shapes
// put together in one space, each moved and turned on its own, and a derived
// object made from them: a Container Fill (an insert: the inside of a
// container, up to a level, kept off its walls), cut by the objects to go in
// it (each kept off by a margin; the space above it left open, or just round
// it; the cut smoothed if asked, never into the margin), kept in one piece.
// The fill is worked on as voxels (little cubes, res apart) and turned into a
// mesh (surface nets) to show and to save.
//
// three.js: meters, y up; the page shows Blender's axes (X, Y, Z up: x, −z,
// y here) in mm.
import * as THREE from "three";
import { OrbitControls } from "./vendor/OrbitControls.js";
import { loadOBJ, VIEWS } from "./modelview.js";

const SHAPE_MAT = () => new THREE.MeshStandardMaterial({ color: 0xb9bdc5, roughness: 0.85, metalness: 0, side: THREE.DoubleSide });
const MISSING_MAT = () => new THREE.MeshBasicMaterial({ color: 0xffb020, transparent: true, opacity: 0.18, depthWrite: false, side: THREE.DoubleSide });
const CLAY_MAT = new THREE.MeshStandardMaterial({ color: 0xb9bdc5, roughness: 0.85, metalness: 0, side: THREE.DoubleSide }); // (as the model viewer's)
const WIRE_MAT = new THREE.MeshBasicMaterial({ color: 0x7ea6ff, wireframe: true });
const FILL_MAT = () => new THREE.MeshStandardMaterial({ color: 0x2bb5a0, roughness: 0.7, metalness: 0, side: THREE.DoubleSide });
const MAX_VOXELS = 80e6; // (a fill's grid at most: memory)

// Blender's axes (mm) ↔ three's (m).
export const toThree = (b) => new THREE.Vector3(b.x / 1000, b.z / 1000, -b.y / 1000);
export const toBlender = (v) => ({ x: v.x * 1000, y: -v.z * 1000, z: v.y * 1000 });
// rotQuat: a turn of x, y, z degrees about Blender's X, Y, Z (X first, then Y,
// then Z), as a quaternion here.
function rotQuat(r) {
  const d = Math.PI / 180, q = new THREE.Quaternion();
  const qx = new THREE.Quaternion().setFromAxisAngle(new THREE.Vector3(1, 0, 0), r.x * d);
  const qy = new THREE.Quaternion().setFromAxisAngle(new THREE.Vector3(0, 0, -1), r.y * d);
  const qz = new THREE.Quaternion().setFromAxisAngle(new THREE.Vector3(0, 1, 0), r.z * d);
  return q.multiply(qz).multiply(qy).multiply(qx);
}
const tick = () => new Promise((r) => setTimeout(r, 20)); // (the status shown between steps)

// ------------------------------------------------------------- voxel grids
// A grid: o (the corner, m), h (a voxel's side, m), nx × ny × nz voxels
// (y up), v: 1 per voxel in it. Voxel (x, y, z)'s middle: o + (x+½, y+½, z+½)·h.
function grid(o, h, nx, ny, nz) {
  if (nx * ny * nz > MAX_VOXELS) throw new Error(`that's ${Math.round(nx * ny * nz / 1e6)} million voxels: use a coarser resolution`);
  return { o, h, nx, ny, nz, v: new Uint8Array(nx * ny * nz) };
}
const at = (g, x, y, z) => (z * g.ny + y) * g.nx + x;

// rasterize marks the voxels of g that obj's surfaces (as placed) pass
// through: points along each triangle, closer than half a voxel apart.
function rasterize(g, obj) {
  const a = new THREE.Vector3(), b = new THREE.Vector3(), c = new THREE.Vector3(), p = new THREE.Vector3();
  const { o, h, nx, ny, nz, v } = g;
  const mark = (q) => {
    const x = Math.floor((q.x - o.x) / h), y = Math.floor((q.y - o.y) / h), z = Math.floor((q.z - o.z) / h);
    if (x >= 0 && y >= 0 && z >= 0 && x < nx && y < ny && z < nz) v[(z * ny + y) * nx + x] = 1;
  };
  obj.updateMatrixWorld(true);
  obj.traverse((m) => {
    if (!m.isMesh) return;
    const pos = m.geometry.getAttribute("position"), idx = m.geometry.index, mw = m.matrixWorld;
    const T = (idx ? idx.count : pos.count) / 3;
    for (let t = 0; t < T; t++) {
      const i0 = idx ? idx.getX(t * 3) : t * 3, i1 = idx ? idx.getX(t * 3 + 1) : t * 3 + 1, i2 = idx ? idx.getX(t * 3 + 2) : t * 3 + 2;
      a.fromBufferAttribute(pos, i0).applyMatrix4(mw);
      b.fromBufferAttribute(pos, i1).applyMatrix4(mw);
      c.fromBufferAttribute(pos, i2).applyMatrix4(mw);
      const n = Math.max(1, Math.ceil(Math.max(a.distanceTo(b), b.distanceTo(c), c.distanceTo(a)) / (h * 0.45)));
      for (let i = 0; i <= n; i++) for (let j = 0; j <= n - i; j++) {
        const u = i / n, w = j / n;
        p.set(a.x + (b.x - a.x) * u + (c.x - a.x) * w, a.y + (b.y - a.y) * u + (c.y - a.y) * w, a.z + (b.z - a.z) * u + (c.z - a.z) * w);
        mark(p);
      }
    }
  });
}

// edt: each voxel's squared distance (in voxels) to the nearest voxel of the
// set (src[i] true), Felzenszwalb's, one axis at a time.
function edt(g, src) {
  const { nx, ny, nz } = g, N = nx * ny * nz, INF = 1e20, d = new Float32Array(N);
  for (let i = 0; i < N; i++) d[i] = src(i) ? 0 : INF;
  const n = Math.max(nx, ny, nz), f = new Float64Array(n), out = new Float64Array(n), vv = new Int32Array(n), zz = new Float64Array(n + 1);
  const line = (len) => {
    let k = 0;
    vv[0] = 0; zz[0] = -INF; zz[1] = INF;
    for (let q = 1; q < len; q++) {
      let s;
      for (;;) {
        s = ((f[q] + q * q) - (f[vv[k]] + vv[k] * vv[k])) / (2 * q - 2 * vv[k]);
        if (s <= zz[k] && k > 0) k--; else break;
      }
      if (s <= zz[k]) { k = 0; vv[0] = q; zz[0] = -INF; zz[1] = INF; continue; } // (k: 0, still under: q takes over)
      k++; vv[k] = q; zz[k] = s; zz[k + 1] = INF;
    }
    k = 0;
    for (let q = 0; q < len; q++) { while (zz[k + 1] < q) k++; const dq = q - vv[k]; out[q] = dq * dq + f[vv[k]]; }
  };
  for (let z = 0; z < nz; z++) for (let y = 0; y < ny; y++) { // along x
    const b = (z * ny + y) * nx;
    for (let x = 0; x < nx; x++) f[x] = d[b + x];
    line(nx);
    for (let x = 0; x < nx; x++) d[b + x] = out[x];
  }
  for (let z = 0; z < nz; z++) for (let x = 0; x < nx; x++) { // along y
    for (let y = 0; y < ny; y++) f[y] = d[(z * ny + y) * nx + x];
    line(ny);
    for (let y = 0; y < ny; y++) d[(z * ny + y) * nx + x] = out[y];
  }
  for (let y = 0; y < ny; y++) for (let x = 0; x < nx; x++) { // along z
    for (let z = 0; z < nz; z++) f[z] = d[(z * ny + y) * nx + x];
    line(nz);
    for (let z = 0; z < nz; z++) d[(z * ny + y) * nx + x] = out[z];
  }
  return d;
}
// grow: the set g.v, grown by r voxels (in place). shrink: shrunk by r.
function grow(g, r) {
  if (r <= 0) return;
  const d = edt(g, (i) => g.v[i]), r2 = r * r;
  for (let i = 0; i < d.length; i++) if (d[i] <= r2) g.v[i] = 1;
}
function shrink(g, r) {
  if (r <= 0) return;
  const d = edt(g, (i) => !g.v[i]), r2 = r * r;
  for (let i = 0; i < d.length; i++) if (d[i] <= r2) g.v[i] = 0;
}

// flood marks (in out) the voxels joined to seed (face to face) through those
// pass(i) allows; whether it reached the grid's sides (not closed in).
function flood(g, seed, pass, out) {
  const { nx, ny, nz } = g, q = new Int32Array(nx * ny * nz);
  let head = 0, tail = 0, open = false;
  out[seed] = 1; q[tail++] = seed;
  while (head < tail) {
    const i = q[head++], x = i % nx, yz = (i - x) / nx, y = yz % ny, z = (yz - y) / ny;
    if (x === 0 || x === nx - 1 || y === 0 || z === 0 || z === nz - 1) open = true;
    const nb = [x > 0 ? i - 1 : -1, x < nx - 1 ? i + 1 : -1, y > 0 ? i - nx : -1, y < ny - 1 ? i + nx : -1, z > 0 ? i - nx * ny : -1, z < nz - 1 ? i + nx * ny : -1];
    for (const j of nb) if (j >= 0 && !out[j] && pass(j)) { out[j] = 1; q[tail++] = j; }
  }
  return open;
}

// solid: obj (as placed) as voxels on a grid lined up with base's (the same
// size, its corner a whole number of voxels off), round its box and pad
// voxels more: its surface, and all it closes in (what can't be reached from
// outside). Open meshes: their surface, as it is.
function solid(base, obj, pad) {
  const box = new THREE.Box3().setFromObject(obj), h = base.h;
  const lo = [0, 0, 0], n = [0, 0, 0];
  ["x", "y", "z"].forEach((k, i) => {
    lo[i] = Math.floor((box.min[k] - base.o[k]) / h) - pad;
    n[i] = Math.floor((box.max[k] - base.o[k]) / h) + pad + 1 - lo[i];
  });
  const g = grid(new THREE.Vector3(base.o.x + lo[0] * h, base.o.y + lo[1] * h, base.o.z + lo[2] * h), h, n[0], n[1], n[2]);
  g.off = lo;
  rasterize(g, obj);
  const outside = new Uint8Array(g.v.length);
  flood(g, 0, (i) => !g.v[i], outside);
  for (let i = 0; i < g.v.length; i++) g.v[i] = outside[i] ? 0 : 1;
  return g;
}

// keepLargest leaves g's biggest piece (voxels joined face to face); how
// many voxels it took away.
function keepLargest(g) {
  const { nx, ny, nz, v } = g, N = v.length, label = new Int32Array(N), q = new Int32Array(N);
  let best = 0, bestSize = 0, cur = 0;
  for (let s = 0; s < N; s++) {
    if (!v[s] || label[s]) continue;
    cur++;
    let head = 0, tail = 0;
    label[s] = cur; q[tail++] = s;
    while (head < tail) {
      const i = q[head++], x = i % nx, yz = (i - x) / nx, y = yz % ny, z = (yz - y) / ny;
      const nb = [x > 0 ? i - 1 : -1, x < nx - 1 ? i + 1 : -1, y > 0 ? i - nx : -1, y < ny - 1 ? i + nx : -1, z > 0 ? i - nx * ny : -1, z < nz - 1 ? i + nx * ny : -1];
      for (const j of nb) if (j >= 0 && v[j] && !label[j]) { label[j] = cur; q[tail++] = j; }
    }
    if (tail > bestSize) { bestSize = tail; best = cur; }
  }
  let removed = 0;
  for (let i = 0; i < N; i++) if (v[i] && label[i] !== best) { v[i] = 0; removed++; }
  return removed;
}

// surfaceMesh: g's voxels as a closed mesh (surface nets on the voxels
// blurred a little, so faces aren't stair steps), in place (meters).
function surfaceMesh(g) {
  const { nx, ny, nz, h, o } = g, N = nx * ny * nz;
  // The field: each voxel 1 or 0, blurred (1-2-1 each way): the surface where it's ½.
  let f = new Float32Array(N), t = new Float32Array(N);
  for (let i = 0; i < N; i++) f[i] = g.v[i];
  const blur = (stride, len, idxOf) => {
    for (let i = 0; i < N; i++) {
      const c = idxOf(i);
      const a = c > 0 ? f[i - stride] : 0, b = c < len - 1 ? f[i + stride] : 0;
      t[i] = (a + 2 * f[i] + b) / 4;
    }
    [f, t] = [t, f];
  };
  blur(1, nx, (i) => i % nx);
  blur(nx, ny, (i) => Math.floor(i / nx) % ny);
  blur(nx * ny, nz, (i) => Math.floor(i / (nx * ny)));
  const val = (x, y, z) => (x < 0 || y < 0 || z < 0 || x >= nx || y >= ny || z >= nz ? 0 : f[(z * ny + y) * nx + x]);
  // A vertex in each cell (between 8 voxel middles) the surface crosses:
  // where its edges cross ½, averaged. Cells from −1 (round the outside).
  const cx = nx + 1, cy = ny + 1, cz = nz + 1, cell = new Int32Array(cx * cy * cz).fill(-1);
  const verts = [];
  const corner = [[0, 0, 0], [1, 0, 0], [0, 1, 0], [1, 1, 0], [0, 0, 1], [1, 0, 1], [0, 1, 1], [1, 1, 1]];
  const edges = [[0, 1], [2, 3], [4, 5], [6, 7], [0, 2], [1, 3], [4, 6], [5, 7], [0, 4], [1, 5], [2, 6], [3, 7]];
  const cv = new Float32Array(8);
  for (let z = -1; z < nz; z++) for (let y = -1; y < ny; y++) for (let x = -1; x < nx; x++) {
    let ins = 0;
    for (let k = 0; k < 8; k++) { cv[k] = val(x + corner[k][0], y + corner[k][1], z + corner[k][2]); if (cv[k] >= 0.5) ins++; }
    if (ins === 0 || ins === 8) continue;
    let sx = 0, sy = 0, sz = 0, m = 0;
    for (const [i, j] of edges) {
      const a = cv[i], b = cv[j];
      if ((a >= 0.5) === (b >= 0.5)) continue;
      const s = (0.5 - a) / (b - a), ci = corner[i], cj = corner[j];
      sx += ci[0] + (cj[0] - ci[0]) * s; sy += ci[1] + (cj[1] - ci[1]) * s; sz += ci[2] + (cj[2] - ci[2]) * s; m++;
    }
    cell[((z + 1) * cy + (y + 1)) * cx + (x + 1)] = verts.length / 3;
    verts.push(o.x + (x + 0.5 + sx / m) * h, o.y + (y + 0.5 + sy / m) * h, o.z + (z + 0.5 + sz / m) * h);
  }
  const C = (x, y, z) => cell[((z + 1) * cy + (y + 1)) * cx + (x + 1)];
  const tris = [];
  const quad = (a, b, c, d, out) => {
    if (a < 0 || b < 0 || c < 0 || d < 0) return;
    if (out) tris.push(a, b, c, a, c, d); else tris.push(a, c, b, a, d, c);
  };
  // Each voxel edge the surface crosses: the 4 cells round it, a quad facing
  // from in to out.
  for (let z = -1; z < nz; z++) for (let y = -1; y < ny; y++) for (let x = -1; x < nx; x++) {
    const p = val(x, y, z) >= 0.5;
    if (y >= 0 && z >= 0 && p !== (val(x + 1, y, z) >= 0.5)) quad(C(x, y - 1, z - 1), C(x, y, z - 1), C(x, y, z), C(x, y - 1, z), p);
    if (x >= 0 && z >= 0 && p !== (val(x, y + 1, z) >= 0.5)) quad(C(x - 1, y, z - 1), C(x - 1, y, z), C(x, y, z), C(x, y, z - 1), p);
    if (x >= 0 && y >= 0 && p !== (val(x, y, z + 1) >= 0.5)) quad(C(x - 1, y - 1, z), C(x, y - 1, z), C(x, y, z), C(x - 1, y, z), p);
  }
  const geo = new THREE.BufferGeometry();
  geo.setAttribute("position", new THREE.BufferAttribute(new Float32Array(verts), 3));
  geo.setIndex(new THREE.BufferAttribute(verts.length / 3 > 65535 ? new Uint32Array(tris) : new Uint16Array(tris), 1));
  geo.computeVertexNormals();
  return geo;
}

// ------------------------------------------------------------- the space
// createCombine: the editor on canvas. Objects: { id, kind (model | box |
// cylinder | sphere | fill), name, holder (placed: position, turn), visible,
// pos and rot (Blender's, mm and degrees), size (a shape's, mm), fill (a
// fill's: its grid, how it was made, its undo) }.
export function createCombine(canvas) {
  const renderer = new THREE.WebGLRenderer({ canvas, antialias: true, alpha: true });
  renderer.setPixelRatio(Math.min(devicePixelRatio, 2));
  const scene = new THREE.Scene(), camera = new THREE.PerspectiveCamera(40, 1, 0.001, 100);
  scene.add(new THREE.HemisphereLight(0xffffff, 0x8a8f99, 3));
  const lamp = new THREE.DirectionalLight(0xffffff, 1.6);
  lamp.position.set(0.3, 0.6, 1);
  camera.add(lamp);
  scene.add(camera);
  const controls = new OrbitControls(camera, canvas);
  controls.enableDamping = true;
  controls.dampingFactor = 0.12;
  controls.screenSpacePanning = true;
  controls.mouseButtons.LEFT = THREE.MOUSE.PAN; // (as the model viewer: drag moves the view, the wheel turns it)
  controls.mouseButtons.MIDDLE = THREE.MOUSE.ROTATE;
  controls.mouseButtons.RIGHT = THREE.MOUSE.ROTATE;
  controls.zoomToCursor = true;
  const grid10 = new THREE.GridHelper(1, 100, 0x4a5262, 0x2c323d); // (1 m, lines 1 cm apart)
  grid10.material.transparent = true;
  grid10.material.opacity = 0.6;
  scene.add(grid10);
  // Axes: X, Y, Z (Blender's) from the origin, red, green, blue; off at first.
  const axes = new THREE.Group();
  for (const [dir, color] of [[[1, 0, 0], 0xe5484d], [[0, 0, -1], 0x46a758], [[0, 1, 0], 0x3e7bfa]]) {
    const g = new THREE.BufferGeometry().setFromPoints([new THREE.Vector3(), new THREE.Vector3(...dir).multiplyScalar(0.5)]);
    axes.add(new THREE.Line(g, new THREE.LineBasicMaterial({ color, depthTest: false, transparent: true })));
  }
  axes.renderOrder = 5;
  axes.visible = false;
  scene.add(axes);
  const sel = new THREE.BoxHelper(undefined, 0xffb020);
  sel.visible = false;
  scene.add(sel);
  // Marks: the fill's level (a sheet) and its inside point (a dot, an arrow down).
  const sheet = new THREE.Mesh(new THREE.PlaneGeometry(1, 1), new THREE.MeshBasicMaterial({ color: 0x5b8cff, transparent: true, opacity: 0.25, side: THREE.DoubleSide, depthWrite: false }));
  sheet.rotation.x = -Math.PI / 2;
  sheet.visible = false;
  scene.add(sheet);
  const dot = new THREE.Mesh(new THREE.SphereGeometry(1, 16, 12), new THREE.MeshBasicMaterial({ color: 0xff5a5f }));
  dot.visible = false;
  scene.add(dot);
  const arrow = new THREE.ArrowHelper(new THREE.Vector3(0, -1, 0), new THREE.Vector3(), 0.03, 0xff5a5f);
  arrow.visible = false;
  scene.add(arrow);

  const objects = [];
  let seq = 0, selected = null, onChange = () => {}, picking = null, dragging = null;
  const api = {};
  const changed = () => {
    applyLook();
    outline();
    if (ring.visible) api.turnRing(ring.userData.o === selected ? selected : null, ring.userData.ax, ring.userData.sign); // (where it is now; none once it's not selected)
    onChange();
    if (!dragging) queueRecut();
  }; // (a cut thing moved: its fill cut again, once let go)
  api.setOnChange = (f) => { onChange = f; }; // (the page's panel: as it is)

  // History: each step done (what: its name; undo, redo: back and on), the
  // last HIST kept; at: how many are done (the rest: undone, redone in turn;
  // a new step drops them).
  const HIST = 100, hist = [];
  let hat = 0;
  function record(what, undo, redo) {
    hist.length = hat;
    hist.push({ what, undo, redo });
    if (hist.length > HIST) hist.shift();
    hat = hist.length;
  }
  api.undo = () => { if (!hat) return null; const h = hist[--hat]; h.undo(); changed(); return h.what; };
  api.redo = () => { if (hat >= hist.length) return null; const h = hist[hat++]; h.redo(); changed(); return h.what; };
  api.undoName = () => (hat ? hist[hat - 1].what : "");
  api.redoName = () => (hat < hist.length ? hist[hat].what : "");
  // pose: o's place and turn (a copy); posed: o put so.
  const pose = (o) => ({ pos: { ...o.pos }, rot: { ...o.rot } });
  const posed = (o, p) => { o.pos = { ...p.pos }; o.rot = { ...p.rot }; place(o); };
  // moved: o went from pose b to where it is now: a step (none if it didn't).
  function moved(o, b, what = `Move ${o.name}`) {
    const a = pose(o);
    if (JSON.stringify(a) === JSON.stringify(b)) return;
    record(what, () => posed(o, b), () => posed(o, a));
  }
  // detach / attach: o out of the space (kept, to come back) and in again, at i.
  function detach(o) {
    const i = objects.indexOf(o);
    if (i >= 0) objects.splice(i, 1);
    scene.remove(o.holder);
    if (selected === o) selected = null;
    return i;
  }
  function attach(o, i) {
    objects.splice(Math.max(0, Math.min(i, objects.length)), 0, o);
    scene.add(o.holder);
    selected = o;
  }

  // inset: the stage's right side covered (the sidebar), px; the picture
  // shifted left by half of it (eased there), so what's in the middle is in
  // the middle of what's seen (as the model editor does). shift: now.
  let inset = 0, shift = 0, last = 0;
  api.setInset = (px) => { inset = Math.max(0, px); };
  function offset() {
    const w = canvas.clientWidth, h = canvas.clientHeight;
    if (shift > 0.5 && w && h) camera.setViewOffset(w, h, shift, 0, w, h);
    else camera.clearViewOffset();
    camera.updateProjectionMatrix();
  }
  function resize() {
    const w = canvas.clientWidth, h = canvas.clientHeight;
    if (!w || !h) return;
    if (canvas.width !== Math.round(w * renderer.getPixelRatio()) || canvas.height !== Math.round(h * renderer.getPixelRatio())) {
      renderer.setSize(w, h, false);
      camera.aspect = w / h;
      offset();
    }
  }
  function shiftTick(dt) {
    const want = inset / 2;
    if (Math.abs(want - shift) < 0.5) { if (shift !== want) { shift = want; offset(); } return; }
    shift += (want - shift) * Math.min(1, dt * 12);
    offset();
  }
  let running = false;
  function loop(now) {
    if (!running) return;
    resize();
    const dt = last ? Math.min(0.1, (now - last) / 1000) : 1;
    shiftTick(dt);
    glideTick(last ? dt : 0);
    last = now;
    controls.update();
    if (sel.visible) sel.update();
    renderer.render(scene, camera);
    requestAnimationFrame(loop);
  }
  api.start = () => { if (!running) { running = true; last = 0; requestAnimationFrame(loop); } };
  api.stop = () => { running = false; };

  // outline: the selected one's box (none for one with nothing in it yet, a
  // fill not made: the box helper would keep the last one's).
  function outline() {
    sel.visible = !!selected?.visible && !new THREE.Box3().setFromObject(selected.holder).isEmpty();
    if (sel.visible) sel.setFromObject(selected.holder);
  }
  // place: o's holder where its pos and rot say.
  function place(o) {
    o.holder.position.copy(toThree(o.pos));
    o.holder.quaternion.copy(rotQuat(o.rot));
    o.holder.updateMatrixWorld(true);
  }
  // add puts an object in: inner (its mesh or meshes) centered on the
  // holder, standing on it (so its pos is its bottom middle), set beside the
  // others.
  function add(kind, name, inner, extra = {}) {
    const holder = new THREE.Group();
    const box = new THREE.Box3().setFromObject(inner), c = box.getCenter(new THREE.Vector3());
    inner.position.sub(new THREE.Vector3(c.x, box.min.y, c.z));
    holder.add(inner);
    scene.add(holder);
    const dims = box.getSize(new THREE.Vector3()).toArray(); // (its size as it is, not turned: m; a missing model's box, from a work file)
    const o = { id: ++seq, kind, name, holder, inner, visible: true, pos: { x: 0, y: 0, z: 0 }, rot: { x: 0, y: 0, z: 0 }, dims, geo: 0, ...extra };
    if (kind !== "fill") { // (beside the rest: along X, 2 cm apart)
      let right = -Infinity;
      for (const p of objects) if (p.visible && p.kind !== "fill") right = Math.max(right, new THREE.Box3().setFromObject(p.holder).max.x);
      const w = box.max.x - box.min.x;
      o.pos.x = objects.length && Number.isFinite(right) ? (right + 0.02 + w / 2) * 1000 : 0;
    }
    objects.push(o);
    place(o);
    selected = o;
    if (objects.length === 1) api.fit();
    const i = objects.length - 1;
    record(`Add ${name}`, () => detach(o), () => attach(o, i));
    changed();
    return o;
  }
  // addModel: a model (extra: kept on it: ref, the capture and model it's from).
  api.addModel = async (name, base, file, scale, status, extra = {}) => {
    const obj = await loadOBJ(base, file, status);
    obj.scale.setScalar(scale);
    const inner = new THREE.Group();
    inner.add(obj);
    return add("model", name, inner, extra);
  };
  // addMissing: a model a work file names that isn't there any more (renamed,
  // moved, deleted): a see-through box its size (dims, m) in its place,
  // marked missing (not cut out of a fill), till it's linked again.
  api.addMissing = (name, dims, extra = {}) => {
    const geo = new THREE.BoxGeometry(...dims), inner = new THREE.Group();
    inner.add(new THREE.Mesh(geo, MISSING_MAT()));
    inner.add(new THREE.LineSegments(new THREE.EdgesGeometry(geo), new THREE.LineBasicMaterial({ color: 0xffb020 })));
    return add("model", name, inner, { ...extra, missing: true });
  };
  // relink: o's model another one (name, base, file, scale; extra: its ref),
  // where o is, as it's turned; a missing one found again. A step.
  api.relink = async (o, name, base, file, scale, extra = {}, status = () => {}) => {
    const obj = await loadOBJ(base, file, status);
    obj.scale.setScalar(scale);
    const inner = new THREE.Group();
    inner.add(obj);
    const box = new THREE.Box3().setFromObject(inner), c = box.getCenter(new THREE.Vector3());
    inner.position.sub(new THREE.Vector3(c.x, box.min.y, c.z)); // (centered on its place, standing on it: as add puts one)
    const was = { inner: o.inner, name: o.name, missing: !!o.missing, ref: o.ref, dims: o.dims };
    const now = { inner, name, missing: false, ref: extra.ref, dims: box.getSize(new THREE.Vector3()).toArray() };
    const put = (x) => {
      o.holder.remove(o.inner);
      Object.assign(o, x);
      o.holder.add(o.inner);
      o.geo++; // (its cuts worked out again)
      place(o);
    };
    put(now);
    record(`Re-link ${name}`, () => put(was), () => put(now));
    changed();
  };
  // clear: nothing in the space, no steps to undo (a work file opened next).
  api.clear = () => {
    for (const o of [...objects]) detach(o);
    hist.length = hat = 0;
    selected = null;
    changed();
  };
  api.clearHistory = () => { hist.length = hat = 0; changed(); };
  // picture: the view as it is, as a JPEG data URL w px across (a work file's).
  api.picture = (w = 480) => {
    renderer.render(scene, camera);
    const c = document.createElement("canvas"), r = canvas.width / canvas.height;
    c.width = w; c.height = Math.round(w / r);
    const g = c.getContext("2d");
    g.fillStyle = "#14171d";
    g.fillRect(0, 0, c.width, c.height);
    g.drawImage(canvas, 0, 0, c.width, c.height);
    return c.toDataURL("image/jpeg", 0.8);
  };
  // A shape: size in mm (box: w, d, h; cylinder: d, h; sphere: d).
  function shapeGeo(kind, s) {
    const m = 1 / 1000;
    if (kind === "box") return new THREE.BoxGeometry(s.w * m, s.h * m, s.d * m);
    if (kind === "cylinder") return new THREE.CylinderGeometry(s.d * m / 2, s.d * m / 2, s.h * m, 64);
    return new THREE.SphereGeometry(s.d * m / 2, 48, 32);
  }
  api.addShape = (kind) => {
    const size = kind === "box" ? { w: 50, d: 50, h: 50 } : kind === "cylinder" ? { d: 50, h: 50 } : { d: 50 };
    const mesh = new THREE.Mesh(shapeGeo(kind, size), SHAPE_MAT()), inner = new THREE.Group();
    inner.add(mesh);
    const n = objects.filter((o) => o.kind === kind).length + 1;
    return add(kind, `${kind[0].toUpperCase() + kind.slice(1)} ${n}`, inner, { size });
  };
  function sized(o, size) {
    const mesh = o.inner.children[0];
    mesh.geometry.dispose();
    mesh.geometry = shapeGeo(o.kind, size);
    o.size = { ...size };
    mesh.geometry.computeBoundingBox(); // (its own: not where it's placed)
    o.inner.position.set(0, -mesh.geometry.boundingBox.min.y, 0); // (still standing on its pos)
    place(o);
  }
  api.setSize = (o, size) => {
    const b = { ...o.size };
    sized(o, size);
    record(`Resize ${o.name}`, () => sized(o, b), () => sized(o, size));
    changed();
  };
  api.objects = () => objects;
  api.selected = () => selected;
  api.select = (o) => { selected = o; changed(); };
  // peekPose: o shown at pos, rot (either: as it is) as a slider moves, not
  // a step yet (setPose then makes it one, from where it was before).
  api.peekPose = (o, pos, rot) => {
    if (o.kind === "fill") return;
    o.anim = null; // (a turn going: stopped where it's set now)
    o.peekFrom ||= pose(o);
    if (pos) o.pos = pos;
    if (rot) o.rot = rot;
    place(o);
    outline();
    if (ring.visible && ring.userData.o === o) api.turnRing(o, ring.userData.ax, ring.userData.sign);
  };
  // turnTo: o turned to rot (degrees), eased there over ms (the ±90°s, the
  // degrees typed); one step, from where it was before. Its rot is the one
  // it's going to at once (another ±90° meanwhile: from there).
  api.turnTo = (o, rot, ms = 320) => {
    if (o.kind === "fill") return;
    o.peekFrom ||= pose(o);
    const qa = o.holder.quaternion.clone(), qb = rotQuat(rot), go = (o.anim = {}), t0 = performance.now();
    o.rot = { ...rot };
    changedQuiet();
    const step = (now) => {
      if (o.anim !== go) return; // (another turn, a slider, a drag: that one's)
      const t = Math.min(1, (now - t0) / ms), e = t < 0.5 ? 2 * t * t : 1 - (-2 * t + 2) ** 2 / 2;
      o.holder.quaternion.slerpQuaternions(qa, qb, e);
      o.holder.updateMatrixWorld(true);
      outline();
      if (t < 1) return requestAnimationFrame(step);
      o.anim = null;
      api.setPose(o, null, rot);
    };
    requestAnimationFrame(step);
  };
  // changedQuiet: the page told, not the fills cut again (yet).
  const changedQuiet = () => { outline(); onChange(); };
  api.setPose = (o, pos, rot) => {
    if (o.kind === "fill") return;
    o.anim = null;
    const b = o.peekFrom || pose(o);
    delete o.peekFrom;
    if (pos) o.pos = pos;
    if (rot) o.rot = rot;
    place(o);
    moved(o, b, rot && !pos ? `Turn ${o.name}` : `Move ${o.name}`);
    changed();
  };
  // drop: o down (or up) so its lowest point is on the floor.
  api.drop = (o) => {
    if (o.kind === "fill") return;
    const b = pose(o), box = new THREE.Box3().setFromObject(o.holder);
    o.pos = { ...o.pos, z: o.pos.z - box.min.y * 1000 };
    place(o);
    moved(o, b, `Drop ${o.name}`);
    changed();
  };
  const shown = (o, on) => { o.visible = on; o.holder.visible = on; };
  api.setVisible = (o, on) => {
    if (o.visible === on) return;
    shown(o, on);
    record(`${on ? "Show" : "Hide"} ${o.name}`, () => shown(o, !on), () => shown(o, on));
    changed();
  };
  // remove: o out of the space (kept: an undo brings it back).
  api.remove = (o) => {
    const i = detach(o);
    if (i < 0) return;
    record(`Remove ${o.name}`, () => attach(o, i), () => detach(o));
    changed();
  };
  api.rename = (o, name) => {
    const b = o.name;
    if (b === name) return;
    o.name = name;
    record(`Rename ${b}`, () => { o.name = b; }, () => { o.name = name; });
    changed();
  };
  // The view: a preset's direction (VIEWS: 3d, front, side, top), all in
  // sight; view() glides there (about 0.6s; the view turned by hand stops it).
  let viewName = "3d", glide = null;
  api.view = (name) => {
    if (!VIEWS[name]) return;
    viewName = name;
    const from = camera.position.clone(), fromT = controls.target.clone();
    api.fit();
    glide = { from, fromT, to: camera.position.clone(), toT: controls.target.clone(), age: 0 };
    camera.position.copy(from); controls.target.copy(fromT);
  };
  controls.addEventListener("start", () => (glide = null));
  function glideTick(dt) {
    if (!glide) return;
    glide.age += dt;
    const f = Math.min(1, glide.age / 0.6), e = f < 0.5 ? 2 * f * f : 1 - (-2 * f + 2) ** 2 / 2;
    // (swung round the middle, not straight across: the distance eased too)
    const a = glide.from.clone().sub(glide.fromT), b = glide.to.clone().sub(glide.toT);
    const la = a.length(), lb = b.length(), q = new THREE.Quaternion().setFromUnitVectors(a.clone().normalize(), b.clone().normalize());
    const dir = a.clone().normalize().applyQuaternion(new THREE.Quaternion().slerp(q, e));
    controls.target.lerpVectors(glide.fromT, glide.toT, e);
    camera.position.copy(controls.target).addScaledVector(dir, la + (lb - la) * e);
    if (f >= 1) glide = null;
  }
  // look: textured (as built), clay (the models gray: the shapes are, the
  // fill keeps its color) or wire (all as their triangles).
  let lookMode = "textured";
  const originals = new WeakMap();
  function applyLook() {
    for (const o of objects) {
      if (o.missing) continue;
      o.inner.traverse((m) => {
        if (!m.isMesh) return;
        if (!originals.has(m)) originals.set(m, m.material);
        m.material = lookMode === "wire" ? WIRE_MAT : lookMode === "clay" && o.kind === "model" ? CLAY_MAT : originals.get(m);
      });
    }
  }
  api.look = (m) => { lookMode = m; applyLook(); };
  // shown: the axes and the ground (the floor's grid), each on or off.
  api.shown = ({ axes: a = axes.visible, ground: g = grid10.visible } = {}) => { axes.visible = a; grid10.visible = g; };
  api.fit = () => {
    glide = null;
    const vis = objects.filter((o) => o.visible);
    const box = new THREE.Box3();
    for (const o of vis) box.expandByObject(o.holder);
    if (box.isEmpty()) box.set(new THREE.Vector3(-0.1, 0, -0.1), new THREE.Vector3(0.1, 0.1, 0.1));
    const c = box.getCenter(new THREE.Vector3()), r = Math.max(box.getBoundingSphere(new THREE.Sphere()).radius, 0.02);
    // (fitted to the part not under the sidebar: the narrower way, across or up)
    const seen = Math.max(0.2, (canvas.clientWidth - inset) / Math.max(1, canvas.clientHeight)), half = THREE.MathUtils.degToRad(camera.fov / 2);
    const dist = r / Math.sin(Math.min(half, Math.atan(Math.tan(half) * seen))) * 1.1;
    camera.position.copy(c).addScaledVector(VIEWS[viewName], dist);
    camera.near = dist / 1000; camera.far = dist * 50;
    camera.updateProjectionMatrix();
    controls.target.copy(c);
    controls.update();
    const s = Math.max(1, Math.ceil(r * 4 * 10) / 10); // (the floor's grid: a few times its size)
    grid10.scale.setScalar(s);
    axes.scale.setScalar(s);
  };

  // ------------------------------------------------------------ pointer
  const ray = new THREE.Raycaster();
  const hitAt = (e, only) => {
    const b = canvas.getBoundingClientRect();
    ray.setFromCamera(new THREE.Vector2(((e.clientX - b.left) / b.width) * 2 - 1, -((e.clientY - b.top) / b.height) * 2 + 1), camera);
    const list = (typeof only === "function" ? objects.filter(only) : only ? [only] : objects).filter((o) => o.visible).map((o) => o.holder);
    const h = ray.intersectObjects(list, true)[0];
    if (!h) return null;
    const o = objects.find((x) => x.holder === h.object || x.holder.getObjectById(h.object.id));
    return { o, point: h.point };
  };
  // planeAt: where the pointer is on the level y (m), if it's over it.
  const planeAt = (e, y) => {
    const b = canvas.getBoundingClientRect();
    ray.setFromCamera(new THREE.Vector2(((e.clientX - b.left) / b.width) * 2 - 1, -((e.clientY - b.top) / b.height) * 2 + 1), camera);
    const p = ray.ray.intersectPlane(new THREE.Plane(new THREE.Vector3(0, 1, 0), -y), new THREE.Vector3());
    return p && { o: null, point: p };
  };
  // pick: the next click on an object (only: that one, or those it says yes
  // to) gives the point and the object; with plane (y, m), the point clicked
  // on that level instead.
  // (what: what's being picked, for the page to show; picking starting or
  // ending: the page told)
  api.pick = (only, done, plane = null, what = null) => { picking = { only, done, plane, what }; canvas.style.cursor = "crosshair"; onChange(); };
  api.unpick = () => { const was = picking; picking = null; canvas.style.cursor = ""; if (was) onChange(); };
  api.pickWhat = () => picking?.what ?? null;
  api.picking = () => !!picking;
  api.boxOf = (o) => new THREE.Box3().setFromObject(o.holder);
  canvas.addEventListener("pointerdown", (e) => {
    if (e.button !== 0) return;
    const h = picking?.plane != null ? planeAt(e, picking.plane) : hitAt(e, picking?.only);
    if (picking) {
      if (h) { e.stopImmediatePropagation(); controls.enabled = false; const p = picking; api.unpick(); p.done(h.point, h.o); setTimeout(() => (controls.enabled = true)); }
      return;
    }
    // Only the selected one, grabbed, is dragged (⌘ or Ctrl held: the view
    // turned instead); otherwise the view moves (or turns, ⌘/Ctrl/⇧ held),
    // and a click (not a drag) selects what it's on (on nothing: none).
    if (!h || h.o !== selected || h.o.kind === "fill" || e.ctrlKey || e.metaKey) { clickDown = { x: e.clientX, y: e.clientY, o: h?.o ?? null }; return; }
    // Dragging the selected one: along the floor, at the height it was
    // grabbed; with ⇧ held, up and down (Z) instead.
    controls.enabled = false;
    try { canvas.setPointerCapture(e.pointerId); } catch {} // (a pointer it can't hold: dragged all the same)
    if (h.o.anim) api.setPose(h.o, null, h.o.rot); // (a turn going: done at once, its step kept)
    dragging = { o: h.o, start: pose(h.o) };
    anchor(h.point, e.shiftKey);
  }, { capture: true });
  // anchor: the drag from point p on, as it is now: along the floor, or up
  // (an upright sheet through p, facing the camera). (⇧ pressed or let go
  // mid-drag: anchored again where it is, so it doesn't jump.)
  function anchor(p, up) {
    const d = dragging;
    d.up = up;
    d.from = p.clone();
    d.pos = { ...d.o.pos };
    if (!up) d.plane = new THREE.Plane(new THREE.Vector3(0, 1, 0), -p.y);
    else {
      const n = camera.getWorldDirection(new THREE.Vector3()).setY(0);
      if (n.lengthSq() < 1e-9) n.set(0, 0, 1); // (looking straight down: any upright sheet)
      d.plane = new THREE.Plane().setFromNormalAndCoplanarPoint(n.normalize(), p);
    }
  }
  const rayAt = (e) => {
    const b = canvas.getBoundingClientRect();
    ray.setFromCamera(new THREE.Vector2(((e.clientX - b.left) / b.width) * 2 - 1, -((e.clientY - b.top) / b.height) * 2 + 1), camera);
    return ray.ray;
  };
  canvas.addEventListener("pointermove", (e) => {
    if (!dragging) return;
    let p = rayAt(e).intersectPlane(dragging.plane, new THREE.Vector3());
    if (!p) return;
    if (e.shiftKey !== dragging.up) { // (⇧ changed: from here on, the other way)
      anchor(p, e.shiftKey);
      p = rayAt(e).intersectPlane(dragging.plane, new THREE.Vector3());
      if (!p) return;
    }
    const d = p.sub(dragging.from);
    dragging.o.pos = dragging.up
      ? { ...dragging.pos, z: dragging.pos.z + d.y * 1000 }
      : { x: dragging.pos.x + d.x * 1000, y: dragging.pos.y - d.z * 1000, z: dragging.pos.z };
    place(dragging.o);
    changed();
  });
  let clickDown = null;
  canvas.addEventListener("pointerup", (e) => {
    const d = clickDown;
    clickDown = null;
    if (!d || Math.hypot(e.clientX - d.x, e.clientY - d.y) > 4) return; // (a drag of the view)
    if (d.o !== selected) { selected = d.o; changed(); }
  });
  const endDrag = () => {
    if (!dragging) return;
    const d = dragging;
    dragging = null;
    controls.enabled = true;
    moved(d.o, d.start); // (the whole drag: one step)
    changed();
  };
  canvas.addEventListener("pointerup", endDrag);
  canvas.addEventListener("pointercancel", endDrag);

  // ------------------------------------------------------------ turn ring
  // turnRing: a circle round o along which its ax slider (Blender's X, Y or
  // Z, as its rot applies them: X first, then Y, then Z) turns it, an
  // arrowhead on it the way it'd turn (sign: 1, up; −1, down; 0: no arrow);
  // none (ax null).
  const ring = new THREE.Group();
  ring.visible = false;
  ring.renderOrder = 10;
  scene.add(ring);
  const RING_COLOR = { x: 0xe5484d, y: 0x46a758, z: 0x3e7bfa };
  api.turnRing = (o, ax, sign = 1) => {
    const key = o && JSON.stringify([o.id, ax, sign, o.pos, o.rot, o.size ?? null]);
    if (ring.visible && key && ring.userData.key === key) return; // (as it is: not made again)
    for (const c of [...ring.children]) { ring.remove(c); c.geometry.dispose(); c.material.dispose(); } // (a geometry shared by two: disposing twice is harmless)
    ring.visible = !!(o && ax && objects.includes(o));
    if (!ring.visible) return;
    ring.userData = { o, ax, sign, key };
    const d = Math.PI / 180, r = o.rot;
    const qz = new THREE.Quaternion().setFromAxisAngle(new THREE.Vector3(0, 1, 0), r.z * d);
    const qy = new THREE.Quaternion().setFromAxisAngle(new THREE.Vector3(0, 0, -1), r.y * d);
    const axis = ax === "z" ? new THREE.Vector3(0, 1, 0)
      : ax === "y" ? new THREE.Vector3(0, 0, -1).applyQuaternion(qz)
      : new THREE.Vector3(1, 0, 0).applyQuaternion(qy).applyQuaternion(qz);
    o.holder.updateMatrixWorld(true);
    const at = o.holder.getWorldPosition(new THREE.Vector3()), box = new THREE.Box3().setFromObject(o.inner);
    let R = 0.01;
    for (let i = 0; i < 8; i++) R = Math.max(R, new THREE.Vector3(i & 1 ? box.max.x : box.min.x, i & 2 ? box.max.y : box.min.y, i & 4 ? box.max.z : box.min.z).distanceTo(at));
    R *= 1.08;
    // (lit and shaded, hidden where it's behind things; and faintly over
    // everything, so what's behind still shows)
    const lit = () => new THREE.MeshStandardMaterial({ color: RING_COLOR[ax], roughness: 0.35, metalness: 0.1, emissive: RING_COLOR[ax], emissiveIntensity: 0.15 });
    const ghost = () => new THREE.MeshBasicMaterial({ color: RING_COLOR[ax], depthTest: false, depthWrite: false, transparent: true, opacity: 0.25 });
    const add = (geo, place = () => {}) => {
      for (const [mat, order] of [[ghost(), 9], [lit(), 10]]) { const m = new THREE.Mesh(geo, mat); m.renderOrder = order; place(m); ring.add(m); }
    };
    add(new THREE.TorusGeometry(R, R * 0.022, 24, 160));
    if (sign) for (let k = 0; k < 3; k++) { // (arrowheads round it: at angle a, along it the way it'd turn: + about +Z, counterclockwise)
      const a = (k * 2 * Math.PI) / 3, cone = new THREE.ConeGeometry(R * 0.06, R * 0.16, 32);
      add(cone, (m) => { m.position.set(R * Math.cos(a), R * Math.sin(a), 0); m.rotation.z = a + (sign < 0 ? Math.PI : 0); });
    }
    ring.position.copy(at);
    ring.quaternion.setFromUnitVectors(new THREE.Vector3(0, 0, 1), axis);
  };

  // ------------------------------------------------------------ marks
  // marks: the fill's level (y, m) over box, and its inside point.
  api.marks = (level, box, inside) => {
    sheet.visible = level != null && !!box;
    if (sheet.visible) {
      const s = box.getSize(new THREE.Vector3()), c = box.getCenter(new THREE.Vector3());
      sheet.scale.set(s.x * 1.1, s.z * 1.1, 1);
      sheet.position.set(c.x, level, c.z);
    }
    dot.visible = arrow.visible = !!inside;
    if (inside) {
      const r = box ? Math.max(0.002, box.getSize(new THREE.Vector3()).length() / 150) : 0.003;
      dot.scale.setScalar(r);
      dot.position.copy(inside);
      arrow.position.copy(inside);
      arrow.setLength(r * 8, r * 3, r * 2);
    }
  };

  // ------------------------------------------------------------ fill
  // A fill: an object of its own, set (set: its container, top: the fill
  // line picked (y, m), offset: it moved from there (m, + down), inside: a
  // point over the inside (x, z), tol (m) off the walls, res (m) voxels),
  // then made (g: its voxels, base: before its cuts). Its level: top − offset.
  const FILL_SET = { container: null, top: null, offset: 0, inside: null, tol: 0.001, res: 0.001 };
  api.fillLevel = (f) => (f.fill.set.top == null ? null : f.fill.set.top - f.fill.set.offset);
  // addFill: a fill, not set yet.
  api.addFill = () => {
    const mesh = new THREE.Mesh(new THREE.BufferGeometry(), FILL_MAT()), inner = new THREE.Group(), holder = new THREE.Group();
    inner.add(mesh);
    holder.add(inner);
    scene.add(holder);
    const n = objects.filter((o) => o.kind === "fill").length + 1;
    const f = { id: ++seq, kind: "fill", name: `Fill ${n}`, holder, inner, visible: true, pos: { x: 0, y: 0, z: 0 }, rot: { x: 0, y: 0, z: 0 },
      fill: { set: { ...FILL_SET }, g: null, base: null, madeWith: null, gen: 0, cuts: new Map() } };
    f.fill.made = fillKey(f);
    objects.push(f);
    selected = f;
    const i = objects.length - 1;
    record(`Add ${f.name}`, () => detach(f), () => attach(f, i));
    changed();
    return f;
  };
  // setFill: f's settings changed (ch: some of FILL_SET's); a step.
  api.setFill = (f, ch, what = `Set ${f.name}`) => {
    const b = f.fill.set, a = { ...b, ...ch };
    if (Object.keys(ch).every((k) => a[k] === b[k] || (k === "inside" && JSON.stringify(a[k]) === JSON.stringify(b[k])))) return;
    f.fill.set = a;
    record(what, () => { f.fill.set = b; }, () => { f.fill.set = a; });
    changed();
  };
  // fillState: what f has to show: "unset" (its settings not all there),
  // "new" (not made), "changed" (set otherwise since made), "made".
  const setKey = (st) => JSON.stringify({ ...st, container: st.container?.id ?? null });
  api.fillState = (f) => {
    const st = f.fill.set;
    if (!st.container || st.top == null || !st.inside) return "unset";
    return !f.fill.g ? "new" : f.fill.madeWith !== setKey(st) ? "changed" : "made";
  };
  // makeFill: f made (again) as it's set: the inside of its container (as
  // placed now) up to its level, from its inside point, kept tol off the
  // walls, voxels res apart; its cuts made in it. How many voxels the islands
  // left took.
  api.makeFill = (f, status = () => {}) => serial(async () => {
    const F = f.fill, st = { ...F.set };
    if (!st.container || st.top == null || !st.inside) throw new Error("pick its container and fill line first");
    if (!objects.includes(st.container)) throw new Error(`its container (${st.container.name}) isn't in the space`);
    if (st.container.missing) throw new Error(`its container (${st.container.name}) is missing: Re-link it (in Objects) first`);
    const { g } = await fillVoxels(st.container, st.top - st.offset, st.inside, st.tol, st.res, status);
    const b = { g: F.g, base: F.base, madeWith: F.madeWith }, a = { g, base: g.v.slice(), madeWith: setKey(st) };
    const put = (x) => { Object.assign(F, x); F.gen++; if (!F.g) clearMesh(f); }; // (gen: its cuts worked out again, for this grid)
    put(a);
    record(`Make ${f.name}`, () => put(b), () => put(a));
    return { removed: await recut(f, status) };
  });
  function clearMesh(f) {
    const m = f.inner.children[0];
    m.geometry.dispose();
    m.geometry = new THREE.BufferGeometry();
  }
  // fillVoxels: makeFill's voxels (g), and how many the islands left took.
  async function fillVoxels(container, level, inside, tol, res, status) {
    status("Fill: placing the container's walls");
    await tick();
    container.holder.updateMatrixWorld(true);
    const box = new THREE.Box3().setFromObject(container.holder), h = res, pad = Math.ceil(tol / h) + 3;
    if (level <= box.min.y) throw new Error("the level is under the container");
    const ny = Math.ceil((level - box.min.y) / h) + pad;
    const o = new THREE.Vector3(box.min.x - pad * h, level - ny * h, box.min.z - pad * h); // (its top: the level, exactly)
    const g = grid(o, h, Math.ceil((box.max.x - box.min.x) / h) + 2 * pad, ny, Math.ceil((box.max.z - box.min.z) / h) + 2 * pad);
    rasterize(g, container.holder);
    status("Fill: keeping it off the walls");
    await tick();
    const d = edt(g, (i) => g.v[i]), r = tol / h + 0.5, r2 = r * r;
    const seedAt = (y) => at(g, Math.floor((inside.x - o.x) / h), y, Math.floor((inside.z - o.z) / h));
    const ix = Math.floor((inside.x - o.x) / h), iz = Math.floor((inside.z - o.z) / h);
    if (ix < 0 || iz < 0 || ix >= g.nx || iz >= g.nz) throw new Error("the point is off the container: pick one over its inside");
    let sy = g.ny - 1;
    while (sy >= 0 && d[seedAt(sy)] <= r2) sy--; // (the point on the level: down out of a wall or its margin)
    if (sy < 0) throw new Error("no room there: pick a point well inside the container");
    status("Fill: filling the inside");
    await tick();
    const fill = new Uint8Array(g.v.length);
    const open = flood(g, seedAt(sy), (i) => d[i] > r2, fill);
    if (open) throw new Error("the fill runs out of the container (a gap in its walls under the level?): try a bigger tolerance or another point");
    g.v = fill;
    return { g, removed: keepLargest(g) };
  }
  // Cuts stay live: a fill keeps its voxels as made (base) and its cuts (obj
  // → opts: { offset, keepAbove, smooth }; idx: the voxels it takes, worked
  // out with obj at key). Moved, turned, resized, taken out or put back: the
  // fill's cut again from its base (in the background, one at a time).
  const cutKey = (o) => JSON.stringify([objects.includes(o) && !o.missing, o.pos, o.rot, o.size ?? null, o.geo]);
  const fillKey = (f) => JSON.stringify([f.fill.gen, ...[...f.fill.cuts].map(([o, c]) => [o.id, cutKey(o), c.opts])]);
  // cutVoxels: the voxels of F obj takes (as placed): it grown by offset (m),
  // smoothed by smooth (m: its hollows filled in, never cutting less), and
  // with keepAbove everything over it too (so it can go in from above); and
  // with colMax (m), the fill's columns standing inside it cut down to colMax
  // over the cut round them (see domes). base: the fill's voxels as made.
  async function cutVoxels(F, base, obj, { offset, keepAbove, smooth, colMax = null }, status) {
    const h = F.h, gr = Math.ceil(offset / h), sm = Math.ceil(smooth / h);
    status(`Cut: ${obj.name} as voxels`);
    await tick();
    const s = solid(F, obj.holder, gr + 2 * sm + 3);
    status(`Cut: ${obj.name} grown by the margin`);
    await tick();
    grow(s, offset / h + 0.5);
    if (sm > 0) {
      status(`Cut: ${obj.name} smoothed`);
      await tick();
      grow(s, smooth / h); // (closing: grown, then shrunk back as much: hollows filled, never less than it was)
      shrink(s, smooth / h);
    }
    const [ox, oy, oz] = s.off, out = [], lows = new Int32Array(s.nx * s.nz).fill(-1);
    for (let z = 0; z < s.nz; z++) for (let x = 0; x < s.nx; x++) {
      const fx = x + ox, fz = z + oz;
      if (fx < 0 || fz < 0 || fx >= F.nx || fz >= F.nz) continue;
      let low = -1;
      for (let y = 0; y < s.ny; y++) {
        if (!s.v[(z * s.ny + y) * s.nx + x]) continue;
        if (low < 0) low = y;
        const fy = y + oy;
        if (!keepAbove && fy >= 0 && fy < F.ny) out.push(at(F, fx, fy, fz));
      }
      lows[z * s.nx + x] = low;
      if (keepAbove && low >= 0) for (let fy = Math.max(0, low + oy); fy < F.ny; fy++) out.push(at(F, fx, fy, fz));
    }
    if (keepAbove && colMax != null) {
      status(`Cut: ${obj.name}'s inside columns`);
      await tick();
      const ch = Math.round(colMax / h);
      for (const { c, to } of domes(F, base, s, lows)) {
        const fx = (c % s.nx) + ox, fz = Math.floor(c / s.nx) + oz;
        for (let fy = Math.max(0, to + ch); fy < F.ny; fy++) out.push(at(F, fx, fy, fz));
      }
    }
    return Int32Array.from(out);
  }
  // domes: the fill's columns standing inside an Open Above cut: seen from
  // above, the fill's top once cut (t: under the object where it's over the
  // fill, as made elsewhere), then how high a way out from each column to
  // the fill round the cut can stay (r: the highest such way's lowest point,
  // spread from s's edges, all outside it). A column standing over its way
  // out (t > r: a pillar in the object's hollows, the fill in a prop guard's
  // ring) is one: each, c (z * s.nx + x) and to (r, F's y): cut down to
  // there (and colMax over it).
  function domes(F, base, s, lows) {
    const { nx, nz } = s, [ox, oy, oz] = s.off, N = nx * nz, t = new Int32Array(N).fill(-1), r = new Int32Array(N).fill(-2);
    for (let z = 0; z < nz; z++) for (let x = 0; x < nx; x++) {
      const fx = x + ox, fz = z + oz;
      if (fx < 0 || fz < 0 || fx >= F.nx || fz >= F.nz) continue;
      let top = -1;
      for (let fy = F.ny - 1; fy >= 0; fy--) if (base[at(F, fx, fy, fz)]) { top = fy; break; }
      const c = z * nx + x;
      t[c] = lows[c] >= 0 ? Math.min(top, lows[c] + oy - 1) : top;
    }
    // (highest way out first: a queue per height, -1 … F.ny)
    const H = F.ny + 2, buckets = Array.from({ length: H }, () => []);
    let hi = -1;
    const push = (c, v) => { r[c] = v; buckets[v + 1].push(c); if (v + 1 > hi) hi = v + 1; };
    for (let c = 0; c < N; c++) {
      const x = c % nx, z = (c - x) / nx;
      if (x === 0 || z === 0 || x === nx - 1 || z === nz - 1) push(c, t[c]);
    }
    while (hi >= 0) {
      const b = buckets[hi];
      if (!b.length) { hi--; continue; }
      const c = b.pop(), x = c % nx, z = (c - x) / nx, v = r[c];
      if (v + 1 !== hi) continue; // (seen at a higher way already)
      for (const n of [x > 0 ? c - 1 : -1, x < nx - 1 ? c + 1 : -1, z > 0 ? c - nx : -1, z < nz - 1 ? c + nx : -1]) {
        if (n < 0 || r[n] !== -2) continue;
        push(n, Math.min(v, t[n]));
      }
    }
    const out = [];
    for (let c = 0; c < N; c++) if (t[c] > r[c] && r[c] >= 0) out.push({ c, to: r[c] + 1 });
    return out;
  }
  // recut: f from its base, each cut (of things in the space) taken out, then
  // kept in one piece. How many voxels the islands left took.
  async function recut(f, status) {
    if (!f.fill.g) { f.fill.made = fillKey(f); return 0; } // (not made: nothing to cut)
    const F = f.fill.g, v = f.fill.base.slice();
    for (const [o, c] of f.fill.cuts) {
      if (!objects.includes(o) || o.missing) continue; // (a missing model: no cut till it's linked again)
      const key = cutKey(o) + f.fill.gen;
      if (c.key !== key) { c.idx = await cutVoxels(F, f.fill.base, o, c.opts, status); c.key = key; c.hits = c.idx.reduce((n, i) => n + f.fill.base[i], 0); }
      for (const i of c.idx) v[i] = 0;
    }
    status("Cut: keeping it in one piece");
    await tick();
    F.v = v;
    const removed = keepLargest(F);
    status("Cut: making its mesh");
    await tick();
    f.fill.made = fillKey(f);
    remesh(f);
    status("");
    return removed;
  }
  // serial: jobs on the fills, one after another.
  let chain = Promise.resolve(), queued = false, recutting = 0, onStatus = () => {};
  const serial = (job) => (chain = chain.then(job, job));
  api.setOnStatus = (f) => { onStatus = f; };
  api.recutting = () => recutting > 0;
  function queueRecut() {
    if (queued || !objects.some((o) => o.kind === "fill" && o.fill.made !== fillKey(o))) return;
    queued = true;
    setTimeout(() => serial(async () => {
      queued = false;
      recutting++;
      changed();
      onStatus("Cut again…", "");
      let removed = 0;
      try {
        for (const f of objects.filter((o) => o.kind === "fill" && o.fill.made !== fillKey(o))) removed += await recut(f, (t) => onStatus(t ? t + "…" : "", ""));
        onStatus(removed ? `Cut again (pieces left loose taken out: ${removed} voxels)` : "Cut again", "ok");
      } catch (e) { onStatus(e.message, "bad"); }
      recutting--;
      changed();
    }), 150);
  }
  // setCut: obj's cut in f: opts ({ offset (m), keepAbove, smooth (m) }), or
  // none (null); a step. The fill's cut again in the background.
  api.setCut = (f, obj, opts) => {
    const cuts = f.fill.cuts, b = cuts.get(obj) ?? null, a = opts ? { opts: { ...opts }, key: null } : null;
    if (JSON.stringify(b?.opts ?? null) === JSON.stringify(a?.opts ?? null)) return;
    const put = (c) => (c ? cuts.set(obj, c) : cuts.delete(obj));
    put(a);
    record(`${a ? (b ? "Change the cut of" : "Cut") : "Uncut"} ${obj.name}`, () => put(b), () => put(a));
    changed();
  };
  // cutOf: obj's cut in f: its opts, and missed (it's not in the fill where
  // it is), if it's cut.
  api.cutOf = (f, obj) => {
    const c = f?.fill.cuts.get(obj);
    return c ? { ...c.opts, missed: c.hits === 0 } : null;
  };
  function remesh(f) {
    const m = f.inner.children[0];
    m.geometry.dispose();
    m.geometry = surfaceMesh(f.fill.g);
    changed();
  }

  // ------------------------------------------------------------ export
  // parts: the objects shown, as placed (non-indexed, meters, y up), for
  // modelEdit.write.
  api.parts = () => {
    const out = [];
    for (const o of objects) {
      if (!o.visible) continue;
      o.holder.updateMatrixWorld(true);
      o.holder.traverse((m) => {
        if (!m.isMesh || !m.geometry.getAttribute("position")) return; // (a fill not made yet: nothing)
        const g = (m.geometry.index ? m.geometry.toNonIndexed() : m.geometry.clone());
        const sub = new THREE.BufferGeometry();
        for (const n of ["position", "normal"]) if (g.getAttribute(n)) sub.setAttribute(n, g.getAttribute(n).clone());
        sub.applyMatrix4(m.matrixWorld);
        out.push({ geometry: sub, material: "" });
      });
    }
    return out;
  };
  return api;
}
